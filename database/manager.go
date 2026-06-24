package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/devituz/lagodev/logger"
)

// DriverFactory opens a database/sql.DB for the given DSN.
type DriverFactory func(dsn string) (*sql.DB, Grammar, error)

var (
	registryMu sync.RWMutex
	registry   = map[string]DriverFactory{}
)

// Register installs a driver factory under name. Drivers should call this in
// their package init. Re-registration overwrites silently.
func Register(name string, factory DriverFactory) {
	registryMu.Lock()
	defer registryMu.Unlock()
	registry[name] = factory
}

// Manager holds a set of named connections.
type Manager struct {
	mu          sync.RWMutex
	connections map[string]*Connection
	defaultName string
	log         *logger.Logger
}

// NewManager creates an empty connection manager. The default logger is a
// silent logger; call SetLogger to wire SQL logging.
func NewManager() *Manager {
	return &Manager{
		connections: make(map[string]*Connection),
		log:         logger.New(logger.LevelSilent),
	}
}

// SetLogger replaces the manager's logger and applies it to any existing
// connections.
func (m *Manager) SetLogger(l *logger.Logger) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.log = l
	for _, c := range m.connections {
		c.Log = l
	}
}

// Open creates a connection with the given name and config and registers it.
func (m *Manager) Open(name string, cfg Config) (*Connection, error) {
	if name == "" {
		return nil, errors.New("database: connection name required")
	}
	registryMu.RLock()
	factory, ok := registry[cfg.Driver]
	registryMu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("database: driver %q not registered (import the driver package to register it)", cfg.Driver)
	}
	dsn, err := cfg.BuildDSN()
	if err != nil {
		return nil, err
	}
	loc, err := resolveLocation(cfg.TimeZone)
	if err != nil {
		return nil, fmt.Errorf("database: timezone %q: %w", cfg.TimeZone, err)
	}

	db, grammar, err := factory(dsn)
	if err != nil {
		return nil, fmt.Errorf("database: open %s: %w", name, err)
	}
	applyPool(db, cfg)

	conn := &Connection{
		Name:    name,
		DB:      db,
		Grammar: grammar,
		Config:  cfg,
		Log:     m.log,
		Loc:     loc,
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.connections[name]; exists {
		_ = db.Close()
		return nil, fmt.Errorf("database: connection %q already exists", name)
	}
	m.connections[name] = conn
	if m.defaultName == "" {
		m.defaultName = name
	}
	return conn, nil
}

// Use registers an externally-built connection. Useful when the host
// application owns the *sql.DB lifecycle.
//
// If a connection is already registered under name, it is closed before
// being replaced so its pool is not leaked. Pass a fresh *Connection; the
// previous one must not be used after this call returns.
func (m *Manager) Use(name string, conn *Connection) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if prev, ok := m.connections[name]; ok && prev != conn {
		_ = prev.Close()
	}
	m.connections[name] = conn
	if m.defaultName == "" {
		m.defaultName = name
	}
}

// Connection returns a named connection, or the default if name == "".
func (m *Manager) Connection(name string) (*Connection, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if name == "" {
		name = m.defaultName
	}
	c, ok := m.connections[name]
	if !ok {
		return nil, fmt.Errorf("database: connection %q not found", name)
	}
	return c, nil
}

// SetDefault selects which named connection is returned by Connection("").
func (m *Manager) SetDefault(name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.connections[name]; !ok {
		return fmt.Errorf("database: cannot set default; connection %q not found", name)
	}
	m.defaultName = name
	return nil
}

// Default returns the default connection.
func (m *Manager) Default() (*Connection, error) {
	return m.Connection("")
}

// Close terminates all connections in the manager.
func (m *Manager) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	var first error
	for _, c := range m.connections {
		if err := c.Close(); err != nil && first == nil {
			first = err
		}
	}
	return first
}

// Ping pings every connection.
func (m *Manager) Ping(ctx context.Context) error {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for name, c := range m.connections {
		if err := c.Ping(ctx); err != nil {
			return fmt.Errorf("database: ping %s: %w", name, err)
		}
	}
	return nil
}

func applyPool(db *sql.DB, cfg Config) {
	if cfg.MaxOpenConns > 0 {
		db.SetMaxOpenConns(cfg.MaxOpenConns)
	}
	if cfg.MaxIdleConns > 0 {
		db.SetMaxIdleConns(cfg.MaxIdleConns)
	}
	if cfg.ConnMaxIdleTime > 0 {
		db.SetConnMaxIdleTime(cfg.ConnMaxIdleTime)
	}
	if cfg.ConnMaxLifetime > 0 {
		db.SetConnMaxLifetime(cfg.ConnMaxLifetime)
	}
}

// Global is a process-wide manager. Applications typically use this; tests
// and library code should construct their own Manager.
var Global = NewManager()

// resolveLocation maps Config.TimeZone to a *time.Location.
//
//	"" or "UTC" → time.UTC
//	"Local"     → time.Local
//	"<IANA TZ>" → time.LoadLocation(tz), e.g. "Asia/Tashkent"
func resolveLocation(tz string) (*time.Location, error) {
	switch tz {
	case "", "UTC":
		return time.UTC, nil
	case "Local":
		return time.Local, nil
	}
	return time.LoadLocation(tz)
}
