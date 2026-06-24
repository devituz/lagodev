package observability

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"sync"
	"time"
)

// SpanStatus describes the terminal status of a span.
type SpanStatus int

const (
	// StatusUnset means no explicit status was recorded.
	StatusUnset SpanStatus = iota
	// StatusOK means the operation completed successfully.
	StatusOK
	// StatusError means the operation failed.
	StatusError
)

func (s SpanStatus) String() string {
	switch s {
	case StatusOK:
		return "ok"
	case StatusError:
		return "error"
	default:
		return "unset"
	}
}

// Tracer starts spans. Implementations must be safe for concurrent use.
type Tracer interface {
	// Start begins a new span named name. The returned context carries the
	// new span so that downstream Start calls become children of it.
	Start(ctx context.Context, name string) (context.Context, Span)
}

// Span represents a single timed operation. Implementations must be safe
// for concurrent use.
type Span interface {
	// SetAttr attaches a typed key/value pair to the span.
	SetAttr(key string, value any) Span
	// RecordError marks the span as failed and stores the error message.
	RecordError(err error) Span
	// SetStatus overrides the span status.
	SetStatus(status SpanStatus, desc string) Span
	// End finalizes the span and records its duration.
	End()
	// Context returns the span's W3C trace/span identifiers.
	Context() SpanContext
}

// SpanContext holds the W3C identifiers that identify a span.
type SpanContext struct {
	TraceID  string // 16-byte trace id, lower-hex (32 chars)
	SpanID   string // 8-byte span id, lower-hex (16 chars)
	ParentID string // parent span id, empty for a root span
	Sampled  bool
}

// Valid reports whether the context carries a usable trace and span id.
func (sc SpanContext) Valid() bool {
	return len(sc.TraceID) == 32 && sc.TraceID != zeroTraceID &&
		len(sc.SpanID) == 16 && sc.SpanID != zeroSpanID
}

const (
	zeroTraceID = "00000000000000000000000000000000"
	zeroSpanID  = "0000000000000000"
)

type spanContextKey struct{}

// ContextWithSpanContext returns a copy of ctx carrying sc.
func ContextWithSpanContext(ctx context.Context, sc SpanContext) context.Context {
	return context.WithValue(ctx, spanContextKey{}, sc)
}

// SpanContextFromContext extracts the active SpanContext, if any.
func SpanContextFromContext(ctx context.Context) (SpanContext, bool) {
	sc, ok := ctx.Value(spanContextKey{}).(SpanContext)
	return sc, ok
}

// SpanRecord is an immutable snapshot of a finished span. The ring buffer
// stores these for the Telescope dashboard to read.
type SpanRecord struct {
	Name      string
	Context   SpanContext
	Start     time.Time
	End       time.Time
	Duration  time.Duration
	Status    SpanStatus
	StatusMsg string
	Err       string
	Attrs     map[string]any
}

// defaultTracer is the stdlib Tracer implementation.
type defaultTracer struct {
	ring   *ringBuffer
	logger *slog.Logger // optional: emit finished spans as structured logs
	emit   bool
}

// TracerOption configures a default Tracer.
type TracerOption func(*defaultTracer)

// WithSpanLogger makes the tracer emit each finished span via slog at the
// given logger.
func WithSpanLogger(l *slog.Logger) TracerOption {
	return func(t *defaultTracer) {
		t.logger = l
		t.emit = true
	}
}

// WithRingCapacity sets the number of finished spans retained in memory.
func WithRingCapacity(n int) TracerOption {
	return func(t *defaultTracer) {
		if n > 0 {
			t.ring = newRingBuffer(n)
		}
	}
}

// NewTracer creates the default stdlib Tracer.
func NewTracer(opts ...TracerOption) Tracer {
	t := &defaultTracer{ring: newRingBuffer(256)}
	for _, o := range opts {
		o(t)
	}
	return t
}

// Recorder exposes the in-process span ring buffer. The default Tracer
// implements it; adapters may choose not to.
type Recorder interface {
	// Spans returns a snapshot of the most recent finished spans, oldest first.
	Spans() []SpanRecord
}

func (t *defaultTracer) Spans() []SpanRecord { return t.ring.snapshot() }

func (t *defaultTracer) Start(ctx context.Context, name string) (context.Context, Span) {
	parent, hasParent := SpanContextFromContext(ctx)

	sc := SpanContext{
		SpanID:  newSpanID(),
		Sampled: true,
	}
	if hasParent && parent.Valid() {
		sc.TraceID = parent.TraceID
		sc.ParentID = parent.SpanID
		sc.Sampled = parent.Sampled
	} else {
		sc.TraceID = newTraceID()
	}

	s := &defaultSpan{
		tracer: t,
		name:   name,
		sc:     sc,
		start:  time.Now(),
		status: StatusUnset,
	}
	return ContextWithSpanContext(ctx, sc), s
}

type defaultSpan struct {
	tracer *defaultTracer
	name   string

	mu        sync.Mutex
	sc        SpanContext
	start     time.Time
	status    SpanStatus
	statusMsg string
	err       string
	attrs     map[string]any
	ended     bool
}

func (s *defaultSpan) Context() SpanContext { return s.sc }

func (s *defaultSpan) SetAttr(key string, value any) Span {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.attrs == nil {
		s.attrs = make(map[string]any)
	}
	s.attrs[key] = value
	return s
}

func (s *defaultSpan) RecordError(err error) Span {
	if err == nil {
		return s
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.err = err.Error()
	if s.status == StatusUnset {
		s.status = StatusError
	}
	return s
}

func (s *defaultSpan) SetStatus(status SpanStatus, desc string) Span {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.status = status
	s.statusMsg = desc
	return s
}

func (s *defaultSpan) End() {
	s.mu.Lock()
	if s.ended {
		s.mu.Unlock()
		return
	}
	s.ended = true
	end := time.Now()

	// Copy attrs to avoid sharing the live map.
	var attrs map[string]any
	if len(s.attrs) > 0 {
		attrs = make(map[string]any, len(s.attrs))
		for k, v := range s.attrs {
			attrs[k] = v
		}
	}
	rec := SpanRecord{
		Name:      s.name,
		Context:   s.sc,
		Start:     s.start,
		End:       end,
		Duration:  end.Sub(s.start),
		Status:    s.status,
		StatusMsg: s.statusMsg,
		Err:       s.err,
		Attrs:     attrs,
	}
	s.mu.Unlock()

	if s.tracer.ring != nil {
		s.tracer.ring.add(rec)
	}
	if s.tracer.emit && s.tracer.logger != nil {
		s.tracer.logf(rec)
	}
}

func (t *defaultTracer) logf(rec SpanRecord) {
	attrs := []slog.Attr{
		slog.String("span_name", rec.Name),
		slog.String("trace_id", rec.Context.TraceID),
		slog.String("span_id", rec.Context.SpanID),
		slog.Duration("duration", rec.Duration),
		slog.String("status", rec.Status.String()),
	}
	if rec.Context.ParentID != "" {
		attrs = append(attrs, slog.String("parent_span_id", rec.Context.ParentID))
	}
	if rec.Err != "" {
		attrs = append(attrs, slog.String("error", rec.Err))
	}
	for k, v := range rec.Attrs {
		attrs = append(attrs, slog.Any("attr."+k, v))
	}
	level := slog.LevelInfo
	if rec.Status == StatusError {
		level = slog.LevelError
	}
	t.logger.LogAttrs(context.Background(), level, "span", attrs...)
}

// --- id generation ---

func newTraceID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func newSpanID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// --- ring buffer ---

type ringBuffer struct {
	mu   sync.Mutex
	buf  []SpanRecord
	next int
	full bool
}

func newRingBuffer(capacity int) *ringBuffer {
	return &ringBuffer{buf: make([]SpanRecord, capacity)}
}

func (r *ringBuffer) add(rec SpanRecord) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.buf[r.next] = rec
	r.next++
	if r.next == len(r.buf) {
		r.next = 0
		r.full = true
	}
}

// snapshot returns the records oldest-first.
func (r *ringBuffer) snapshot() []SpanRecord {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.full {
		out := make([]SpanRecord, r.next)
		copy(out, r.buf[:r.next])
		return out
	}
	out := make([]SpanRecord, 0, len(r.buf))
	out = append(out, r.buf[r.next:]...)
	out = append(out, r.buf[:r.next]...)
	return out
}
