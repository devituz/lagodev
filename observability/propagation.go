package observability

import (
	"net/http"
	"strings"
)

// traceparentHeader is the W3C Trace Context header name.
const traceparentHeader = "Traceparent"

// version00 is the only W3C traceparent version this package emits.
const version00 = "00"

// ParseTraceparent parses a W3C traceparent header value of the form
//
//	version "-" trace-id "-" parent-id "-" trace-flags
//
// e.g. "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01".
// It returns the decoded SpanContext and ok=true on success.
func ParseTraceparent(value string) (SpanContext, bool) {
	value = strings.TrimSpace(value)
	parts := strings.Split(value, "-")
	if len(parts) != 4 {
		return SpanContext{}, false
	}
	ver, traceID, parentID, flags := parts[0], parts[1], parts[2], parts[3]

	// Only version 00 is understood; reject the invalid "ff" version.
	if len(ver) != 2 || !isLowerHex(ver) || ver == "ff" {
		return SpanContext{}, false
	}
	if len(traceID) != 32 || !isLowerHex(traceID) || traceID == zeroTraceID {
		return SpanContext{}, false
	}
	if len(parentID) != 16 || !isLowerHex(parentID) || parentID == zeroSpanID {
		return SpanContext{}, false
	}
	if len(flags) != 2 || !isLowerHex(flags) {
		return SpanContext{}, false
	}

	sampled := (hexNibble(flags[1]) & 0x1) == 0x1
	return SpanContext{
		TraceID: traceID,
		SpanID:  parentID,
		Sampled: sampled,
	}, true
}

// FormatTraceparent renders sc as a W3C version-00 traceparent value.
func FormatTraceparent(sc SpanContext) string {
	flags := "00"
	if sc.Sampled {
		flags = "01"
	}
	return version00 + "-" + sc.TraceID + "-" + sc.SpanID + "-" + flags
}

// ExtractHTTP reads the traceparent header from h, returning the remote
// SpanContext if present and valid.
func ExtractHTTP(h http.Header) (SpanContext, bool) {
	v := h.Get(traceparentHeader)
	if v == "" {
		return SpanContext{}, false
	}
	return ParseTraceparent(v)
}

// InjectHTTP writes sc into h as a traceparent header.
func InjectHTTP(h http.Header, sc SpanContext) {
	if !sc.Valid() {
		return
	}
	h.Set(traceparentHeader, FormatTraceparent(sc))
}

func isLowerHex(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return len(s) > 0
}

func hexNibble(c byte) byte {
	switch {
	case c >= '0' && c <= '9':
		return c - '0'
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10
	default:
		return 0
	}
}
