package telescope

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// RequestEntry describes an inbound HTTP request. Middleware fills it in, but
// it can also be recorded directly.
type RequestEntry struct {
	Method   string
	Path     string
	Status   int
	Duration time.Duration
	// IP is the remote address, optional.
	IP string
	// Tags are extra labels merged with any derived tags.
	Tags []string
}

// RecordRequest stores a Request entry. The request id (from ctx, if present)
// is used both as the entry's RequestID and as a correlation tag.
//
// A Request entry marks the end of a request lifecycle: all child entries
// (queries, cache, exceptions) recorded under the same id are already stored
// by the time the wrapping Middleware calls this. The per-request N+1
// accumulator for that id is therefore dropped here so it cannot accumulate
// one stale map per request id over the life of the process.
func (r *Recorder) RecordRequest(ctx context.Context, req RequestEntry) Entry {
	reqID, _ := RequestIDFromContext(ctx)
	tags := append([]string(nil), req.Tags...)
	if req.Status >= 500 {
		tags = append(tags, "server-error")
	} else if req.Status >= 400 {
		tags = append(tags, "client-error")
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if reqID != "" {
		r.dropNPlus(reqID)
	}
	return r.recordLocked(Entry{
		Type:      TypeRequest,
		RequestID: reqID,
		Tags:      tags,
		Payload: map[string]any{
			"method":      req.Method,
			"path":        req.Path,
			"status":      req.Status,
			"ip":          req.IP,
			"duration_ms": durationMillis(req.Duration),
		},
	})
}

// QueryEntry describes a database statement.
type QueryEntry struct {
	SQL      string
	Bindings []any
	Duration time.Duration
	// Connection names the database connection, optional.
	Connection string
	Tags       []string
}

// RecordQuery stores a Query entry. When the same normalised SQL has already
// been recorded under the same request id, the entry is flagged with the
// "n+1" tag and a payload "n_plus_one" boolean carrying the running count.
func (r *Recorder) RecordQuery(ctx context.Context, q QueryEntry) Entry {
	reqID, _ := RequestIDFromContext(ctx)
	tags := append([]string(nil), q.Tags...)

	payload := map[string]any{
		"sql":         q.SQL,
		"bindings":    bindingStrings(q.Bindings),
		"connection":  q.Connection,
		"duration_ms": durationMillis(q.Duration),
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	// N+1 heuristic: count normalised statements per request id. The first
	// occurrence is fine; repeats are the suspicious lazy-loaded N.
	if reqID != "" {
		norm := normalizeSQL(q.SQL)
		seen := r.nplusFor(reqID)
		seen[norm]++
		if n := seen[norm]; n > 1 {
			tags = append(tags, "n+1")
			payload["n_plus_one"] = true
			payload["repeat_count"] = n
		}
	}

	return r.recordLocked(Entry{
		Type:      TypeQuery,
		RequestID: reqID,
		Tags:      tags,
		Payload:   payload,
	})
}

// JobEntry describes a background/queued job execution.
type JobEntry struct {
	Name     string
	Queue    string
	Status   string // e.g. "processed", "failed"
	Duration time.Duration
	Err      error
	Tags     []string
}

// RecordJob stores a Job entry.
func (r *Recorder) RecordJob(ctx context.Context, j JobEntry) Entry {
	reqID, _ := RequestIDFromContext(ctx)
	tags := append([]string(nil), j.Tags...)
	payload := map[string]any{
		"name":        j.Name,
		"queue":       j.Queue,
		"status":      j.Status,
		"duration_ms": durationMillis(j.Duration),
	}
	if j.Err != nil {
		payload["error"] = j.Err.Error()
		tags = append(tags, "failed")
	}
	return r.Record(Entry{
		Type:      TypeJob,
		RequestID: reqID,
		Tags:      tags,
		Payload:   payload,
	})
}

// CacheEntry describes a cache operation.
type CacheEntry struct {
	Operation string // "get", "set", "forget", ...
	Key       string
	Hit       bool
	Value     any
	Tags      []string
}

// RecordCache stores a Cache entry, tagging hits and misses for get operations.
func (r *Recorder) RecordCache(ctx context.Context, c CacheEntry) Entry {
	reqID, _ := RequestIDFromContext(ctx)
	tags := append([]string(nil), c.Tags...)
	payload := map[string]any{
		"operation": c.Operation,
		"key":       c.Key,
		"hit":       c.Hit,
	}
	if c.Value != nil {
		payload["value"] = fmt.Sprintf("%v", c.Value)
	}
	if strings.EqualFold(c.Operation, "get") {
		if c.Hit {
			tags = append(tags, "hit")
		} else {
			tags = append(tags, "miss")
		}
	}
	return r.Record(Entry{
		Type:      TypeCache,
		RequestID: reqID,
		Tags:      tags,
		Payload:   payload,
	})
}

// MailEntry describes an outbound mail message.
type MailEntry struct {
	From    string
	To      []string
	Subject string
	Mailer  string
	Tags    []string
}

// RecordMail stores a Mail entry.
func (r *Recorder) RecordMail(ctx context.Context, m MailEntry) Entry {
	reqID, _ := RequestIDFromContext(ctx)
	return r.Record(Entry{
		Type:      TypeMail,
		RequestID: reqID,
		Tags:      append([]string(nil), m.Tags...),
		Payload: map[string]any{
			"from":    m.From,
			"to":      append([]string(nil), m.To...),
			"subject": m.Subject,
			"mailer":  m.Mailer,
		},
	})
}

// ExceptionEntry describes a recorded error or panic.
type ExceptionEntry struct {
	Err   error
	Class string // optional logical class/category
	Stack string // optional stack trace
	Tags  []string
}

// RecordException stores an Exception entry. A nil Err is ignored and returns
// the zero Entry.
func (r *Recorder) RecordException(ctx context.Context, ex ExceptionEntry) Entry {
	if ex.Err == nil {
		return Entry{}
	}
	reqID, _ := RequestIDFromContext(ctx)
	payload := map[string]any{
		"message": ex.Err.Error(),
		"class":   ex.Class,
	}
	if ex.Stack != "" {
		payload["stack"] = ex.Stack
	}
	return r.Record(Entry{
		Type:      TypeException,
		RequestID: reqID,
		Tags:      append([]string(nil), ex.Tags...),
		Payload:   payload,
	})
}

// LogEntry describes an application log line.
type LogEntry struct {
	Level   string // e.g. "info", "warn", "error"
	Message string
	Context map[string]any // optional structured fields
	Tags    []string
}

// RecordLog stores a Log entry, tagging the entry with its level.
func (r *Recorder) RecordLog(ctx context.Context, l LogEntry) Entry {
	reqID, _ := RequestIDFromContext(ctx)
	tags := append([]string(nil), l.Tags...)
	if l.Level != "" {
		tags = append(tags, strings.ToLower(l.Level))
	}
	payload := map[string]any{
		"level":   l.Level,
		"message": l.Message,
	}
	if len(l.Context) > 0 {
		payload["context"] = l.Context
	}
	return r.Record(Entry{
		Type:      TypeLog,
		RequestID: reqID,
		Tags:      tags,
		Payload:   payload,
	})
}

// --- helpers ---

// durationMillis renders a duration as fractional milliseconds for payloads.
func durationMillis(d time.Duration) float64 {
	return float64(d) / float64(time.Millisecond)
}

// bindingStrings renders bind values as strings so the payload stays JSON-safe
// regardless of the underlying driver value types.
func bindingStrings(bindings []any) []string {
	if len(bindings) == 0 {
		return nil
	}
	out := make([]string, len(bindings))
	for i, b := range bindings {
		out[i] = fmt.Sprintf("%v", b)
	}
	return out
}

// normalizeSQL collapses a statement to a shape that ignores literal values so
// that two queries differing only in their bound values compare equal. It
// lower-cases keywords-insensitively by folding case, squeezes whitespace and
// replaces numeric/quoted literals and "?"/"$n" placeholders with "?".
func normalizeSQL(sql string) string {
	var b strings.Builder
	b.Grow(len(sql))

	runes := []rune(strings.ToLower(sql))
	i := 0
	lastSpace := false
	for i < len(runes) {
		c := runes[i]
		switch {
		case c == '\'' || c == '"':
			// Skip a quoted string literal, emit a single placeholder.
			quote := c
			i++
			for i < len(runes) && runes[i] != quote {
				i++
			}
			if i < len(runes) {
				i++ // closing quote
			}
			b.WriteByte('?')
			lastSpace = false
		case c >= '0' && c <= '9':
			// Skip a numeric literal, emit a single placeholder.
			for i < len(runes) && (runes[i] >= '0' && runes[i] <= '9' || runes[i] == '.') {
				i++
			}
			b.WriteByte('?')
			lastSpace = false
		case c == '$':
			// Postgres positional placeholder "$1" -> "?".
			i++
			for i < len(runes) && runes[i] >= '0' && runes[i] <= '9' {
				i++
			}
			b.WriteByte('?')
			lastSpace = false
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			if !lastSpace {
				b.WriteByte(' ')
				lastSpace = true
			}
			i++
		default:
			b.WriteRune(c)
			lastSpace = false
			i++
		}
	}
	return strings.TrimSpace(b.String())
}
