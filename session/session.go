// Package session provides cookie-backed sessions modelled on
// Laravel's Session facade.
//
// A session is a map[string]any persisted server-side under an opaque
// ID. The client carries the ID in an HttpOnly+Secure cookie. The
// default store is in-memory (suitable for single-replica apps and
// tests); the Store interface is small enough that a Redis or DB
// driver can plug in without changing call sites.
//
// Sessions are short-lived: the cookie's MaxAge equals the store's
// TTL, and stale records are GC'd in the background. To survive
// restarts or run multi-replica, swap NewMemoryStore() for an external
// store implementation.
package session

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/http"
	"sync"
	"time"
)

// ErrMissing is returned by Get when the requested key isn't set.
var ErrMissing = errors.New("session: key not set")

// Store persists per-session data. Implementations must be safe for
// concurrent use.
type Store interface {
	// Read returns the session data for id. ok=false on miss.
	Read(ctx context.Context, id string) (map[string]any, bool, error)
	// Write replaces the session data for id. ttl=0 means use the
	// store's default.
	Write(ctx context.Context, id string, data map[string]any, ttl time.Duration) error
	// Destroy removes the session.
	Destroy(ctx context.Context, id string) error
}

// MemoryStore is a sync.RWMutex-backed in-process Store with lazy
// expiry and a periodic sweeper.
type MemoryStore struct {
	mu      sync.RWMutex
	items   map[string]memoryItem
	default_ time.Duration
	stop    chan struct{}
}

type memoryItem struct {
	data      map[string]any
	expiresAt time.Time
}

// NewMemoryStore returns a MemoryStore with the given default TTL.
// Pass 0 for "no expiry" (entries live until Destroy).
func NewMemoryStore(defaultTTL time.Duration) *MemoryStore {
	s := &MemoryStore{
		items:    make(map[string]memoryItem),
		default_: defaultTTL,
		stop:     make(chan struct{}),
	}
	go s.sweep()
	return s
}

// Close stops the background sweeper.
func (s *MemoryStore) Close() {
	select {
	case <-s.stop:
	default:
		close(s.stop)
	}
}

func (s *MemoryStore) Read(_ context.Context, id string) (map[string]any, bool, error) {
	s.mu.RLock()
	it, ok := s.items[id]
	s.mu.RUnlock()
	if !ok {
		return nil, false, nil
	}
	if !it.expiresAt.IsZero() && time.Now().After(it.expiresAt) {
		s.mu.Lock()
		delete(s.items, id)
		s.mu.Unlock()
		return nil, false, nil
	}
	cp := make(map[string]any, len(it.data))
	for k, v := range it.data {
		cp[k] = v
	}
	return cp, true, nil
}

func (s *MemoryStore) Write(_ context.Context, id string, data map[string]any, ttl time.Duration) error {
	if ttl == 0 {
		ttl = s.default_
	}
	cp := make(map[string]any, len(data))
	for k, v := range data {
		cp[k] = v
	}
	var exp time.Time
	if ttl > 0 {
		exp = time.Now().Add(ttl)
	}
	s.mu.Lock()
	s.items[id] = memoryItem{data: cp, expiresAt: exp}
	s.mu.Unlock()
	return nil
}

func (s *MemoryStore) Destroy(_ context.Context, id string) error {
	s.mu.Lock()
	delete(s.items, id)
	s.mu.Unlock()
	return nil
}

func (s *MemoryStore) sweep() {
	t := time.NewTicker(5 * time.Minute)
	defer t.Stop()
	for {
		select {
		case <-s.stop:
			return
		case <-t.C:
			now := time.Now()
			s.mu.Lock()
			for k, it := range s.items {
				if !it.expiresAt.IsZero() && now.After(it.expiresAt) {
					delete(s.items, k)
				}
			}
			s.mu.Unlock()
		}
	}
}

// Manager owns the cookie configuration and a Store. A single Manager
// is shared across requests; per-request access happens through
// Session below.
type Manager struct {
	store      Store
	cookieName string
	ttl        time.Duration
	secure     bool
	sameSite   http.SameSite
}

// Options configures Manager. Zero values use safe defaults
// (cookie="lagodev_session", TTL=2h, Secure=true, SameSite=Lax).
//
// Note the inverted `Insecure` flag: cookies default to Secure=true so
// production code is correct without extra setup. Pass Insecure=true
// only for local HTTP development.
type Options struct {
	CookieName string
	TTL        time.Duration
	Insecure   bool // when true, cookies are not marked Secure
	SameSite   http.SameSite
}

// NewManager returns a Manager with the given store and options.
func NewManager(store Store, opts Options) *Manager {
	if opts.CookieName == "" {
		opts.CookieName = "lagodev_session"
	}
	if opts.TTL == 0 {
		opts.TTL = 2 * time.Hour
	}
	if opts.SameSite == 0 {
		opts.SameSite = http.SameSiteLaxMode
	}
	return &Manager{
		store:      store,
		cookieName: opts.CookieName,
		ttl:        opts.TTL,
		secure:     !opts.Insecure,
		sameSite:   opts.SameSite,
	}
}

// Start returns the Session for r. If the request carries a valid
// session cookie, the existing data is loaded; otherwise a fresh
// session is created with a new ID. Call Save(w) before responding to
// persist any changes and (re-)issue the cookie.
func (m *Manager) Start(ctx context.Context, r *http.Request) (*Session, error) {
	var (
		id   string
		data map[string]any
		isNew bool
	)
	if ck, err := r.Cookie(m.cookieName); err == nil && ck.Value != "" {
		id = ck.Value
		d, ok, err := m.store.Read(ctx, id)
		if err != nil {
			return nil, err
		}
		if ok {
			data = d
		}
	}
	if data == nil {
		newID, err := generateID()
		if err != nil {
			return nil, err
		}
		id = newID
		data = make(map[string]any)
		isNew = true
	}
	return &Session{m: m, id: id, data: data, isNew: isNew}, nil
}

// Session is the per-request handle. Not safe for concurrent use
// across goroutines.
type Session struct {
	m     *Manager
	id    string
	data  map[string]any
	isNew bool
	dirty bool
}

// ID returns the opaque session identifier.
func (s *Session) ID() string { return s.id }

// IsNew reports whether the session was created during this request
// (no prior cookie or stale cookie).
func (s *Session) IsNew() bool { return s.isNew }

// Get retrieves a value. ok=false when the key is missing.
func (s *Session) Get(key string) (any, bool) {
	v, ok := s.data[key]
	return v, ok
}

// GetString is a convenience for string-typed values; returns "" if
// the key is missing or the wrong type.
func (s *Session) GetString(key string) string {
	if v, ok := s.data[key]; ok {
		if str, ok := v.(string); ok {
			return str
		}
	}
	return ""
}

// Put stores a value, marking the session dirty.
func (s *Session) Put(key string, value any) {
	s.data[key] = value
	s.dirty = true
}

// Forget removes a key.
func (s *Session) Forget(key string) {
	if _, ok := s.data[key]; ok {
		delete(s.data, key)
		s.dirty = true
	}
}

// All returns a copy of the session data.
func (s *Session) All() map[string]any {
	cp := make(map[string]any, len(s.data))
	for k, v := range s.data {
		cp[k] = v
	}
	return cp
}

// Flush clears the session contents (the ID is preserved).
func (s *Session) Flush() {
	if len(s.data) > 0 {
		s.data = make(map[string]any)
		s.dirty = true
	}
}

// Regenerate issues a new session ID, preserving the data. Call after
// privilege escalation (login/logout) to mitigate session fixation.
func (s *Session) Regenerate(ctx context.Context) error {
	newID, err := generateID()
	if err != nil {
		return err
	}
	_ = s.m.store.Destroy(ctx, s.id)
	s.id = newID
	s.dirty = true
	return nil
}

// Save persists pending changes (if any) and writes the cookie. Always
// safe to call.
func (s *Session) Save(ctx context.Context, w http.ResponseWriter) error {
	if s.dirty || s.isNew {
		if err := s.m.store.Write(ctx, s.id, s.data, s.m.ttl); err != nil {
			return err
		}
	}
	http.SetCookie(w, &http.Cookie{
		Name:     s.m.cookieName,
		Value:    s.id,
		Path:     "/",
		HttpOnly: true,
		Secure:   s.m.secure,
		SameSite: s.m.sameSite,
		MaxAge:   int(s.m.ttl.Seconds()),
	})
	return nil
}

// Destroy removes the session from the store and expires the cookie.
func (s *Session) Destroy(ctx context.Context, w http.ResponseWriter) error {
	if err := s.m.store.Destroy(ctx, s.id); err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{
		Name:     s.m.cookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   s.m.secure,
		SameSite: s.m.sameSite,
		MaxAge:   -1,
	})
	s.data = make(map[string]any)
	return nil
}

func generateID() (string, error) {
	var buf [32]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf[:]), nil
}
