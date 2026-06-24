package telescope

import (
	"encoding/json"
	"time"
)

// Type classifies an Entry. The string values are stable and are used as the
// ?type= query-string filter on the dashboard and JSON API.
type Type string

const (
	// TypeRequest is an inbound HTTP request handled by Middleware.
	TypeRequest Type = "request"
	// TypeQuery is a database statement.
	TypeQuery Type = "query"
	// TypeJob is a background/queued job execution.
	TypeJob Type = "job"
	// TypeCache is a cache get/set/hit/miss operation.
	TypeCache Type = "cache"
	// TypeMail is an outbound mail message.
	TypeMail Type = "mail"
	// TypeException is a recorded error or panic.
	TypeException Type = "exception"
	// TypeLog is an application log line.
	TypeLog Type = "log"
)

// Valid reports whether t is one of the known entry types.
func (t Type) Valid() bool {
	switch t {
	case TypeRequest, TypeQuery, TypeJob, TypeCache, TypeMail, TypeException, TypeLog:
		return true
	default:
		return false
	}
}

// Entry is a single immutable record stored in the recorder's ring buffer.
// Typed Record* helpers build the well-known payload shapes, but Record
// accepts any Entry so callers can store custom kinds too.
type Entry struct {
	// ID uniquely identifies the entry within a process. It is assigned by
	// the recorder if left empty.
	ID string `json:"id"`
	// Type classifies the entry.
	Type Type `json:"type"`
	// Time is when the underlying event happened. Defaulted to now if zero.
	Time time.Time `json:"time"`
	// RequestID correlates child entries (queries, cache, exceptions) with
	// the Request entry they occurred under. Empty when out of a request.
	RequestID string `json:"request_id,omitempty"`
	// Tags are short labels for filtering and quick scanning, e.g. "n+1",
	// "slow", "miss".
	Tags []string `json:"tags,omitempty"`
	// Payload holds the kind-specific fields. It is always JSON-serialisable.
	Payload map[string]any `json:"payload,omitempty"`
}

// hasTag reports whether the entry carries tag.
func (e Entry) hasTag(tag string) bool {
	for _, t := range e.Tags {
		if t == tag {
			return true
		}
	}
	return false
}

// Title is a short human-readable summary used in the dashboard list and as
// the detail-page heading. It is best-effort and never panics on a missing
// payload field.
func (e Entry) Title() string {
	switch e.Type {
	case TypeRequest:
		method, _ := e.Payload["method"].(string)
		path, _ := e.Payload["path"].(string)
		if method == "" && path == "" {
			return "request"
		}
		return method + " " + path
	case TypeQuery:
		if sql, ok := e.Payload["sql"].(string); ok {
			return sql
		}
		return "query"
	case TypeJob:
		if name, ok := e.Payload["name"].(string); ok {
			return name
		}
		return "job"
	case TypeCache:
		op, _ := e.Payload["operation"].(string)
		key, _ := e.Payload["key"].(string)
		if op == "" {
			return key
		}
		return op + " " + key
	case TypeMail:
		if subj, ok := e.Payload["subject"].(string); ok {
			return subj
		}
		return "mail"
	case TypeException:
		if msg, ok := e.Payload["message"].(string); ok {
			return msg
		}
		return "exception"
	case TypeLog:
		level, _ := e.Payload["level"].(string)
		msg, _ := e.Payload["message"].(string)
		if level == "" {
			return msg
		}
		return level + " " + msg
	default:
		return string(e.Type)
	}
}

// PrettyPayload renders the payload as indented JSON for the detail view. It
// returns an empty string when there is nothing to show.
func (e Entry) PrettyPayload() string {
	if len(e.Payload) == 0 {
		return ""
	}
	b, err := json.MarshalIndent(e.Payload, "", "  ")
	if err != nil {
		return ""
	}
	return string(b)
}
