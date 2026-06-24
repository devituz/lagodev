package observability

import (
	"net/http"
	"testing"
)

const (
	validTraceID = "4bf92f3577b34da6a3ce929d0e0e4736"
	validSpanID  = "00f067aa0ba902b7"
	validTP      = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
)

func TestParseTraceparent_Valid(t *testing.T) {
	sc, ok := ParseTraceparent(validTP)
	if !ok {
		t.Fatal("valid traceparent must parse")
	}
	if sc.TraceID != validTraceID {
		t.Fatalf("TraceID = %q, want %q", sc.TraceID, validTraceID)
	}
	if sc.SpanID != validSpanID {
		t.Fatalf("SpanID = %q, want %q", sc.SpanID, validSpanID)
	}
	if !sc.Sampled {
		t.Fatal("flags 01 must set Sampled=true")
	}
}

func TestParseTraceparent_SampledFlag(t *testing.T) {
	sc, ok := ParseTraceparent("00-" + validTraceID + "-" + validSpanID + "-00")
	if !ok {
		t.Fatal("flags 00 must still parse")
	}
	if sc.Sampled {
		t.Fatal("flags 00 must set Sampled=false")
	}
	// Even flag value -> not sampled; only LSB matters.
	sc2, _ := ParseTraceparent("00-" + validTraceID + "-" + validSpanID + "-02")
	if sc2.Sampled {
		t.Fatal("flags 02 (LSB 0) must be Sampled=false")
	}
	sc3, _ := ParseTraceparent("00-" + validTraceID + "-" + validSpanID + "-ff")
	if !sc3.Sampled {
		t.Fatal("flags ff (LSB 1) must be Sampled=true")
	}
}

func TestParseTraceparent_TrimsWhitespace(t *testing.T) {
	if _, ok := ParseTraceparent("  " + validTP + "\t"); !ok {
		t.Fatal("surrounding whitespace must be trimmed")
	}
}

func TestParseTraceparent_Malformed(t *testing.T) {
	cases := map[string]string{
		"empty":            "",
		"too few parts":    "00-" + validTraceID + "-" + validSpanID,
		"too many parts":   validTP + "-extra",
		"version ff":       "ff-" + validTraceID + "-" + validSpanID + "-01",
		"version too long": "000-" + validTraceID + "-" + validSpanID + "-01",
		"version non-hex":  "0g-" + validTraceID + "-" + validSpanID + "-01",
		"short trace id":   "00-abc-" + validSpanID + "-01",
		"zero trace id":    "00-" + zeroTraceID + "-" + validSpanID + "-01",
		"upper hex trace":  "00-4BF92F3577B34DA6A3CE929D0E0E4736-" + validSpanID + "-01",
		"short span id":    "00-" + validTraceID + "-abc-01",
		"zero span id":     "00-" + validTraceID + "-" + zeroSpanID + "-01",
		"bad flags len":    "00-" + validTraceID + "-" + validSpanID + "-1",
		"non-hex flags":    "00-" + validTraceID + "-" + validSpanID + "-zz",
	}
	for name, v := range cases {
		if _, ok := ParseTraceparent(v); ok {
			t.Errorf("%s: ParseTraceparent(%q) returned ok=true, want false", name, v)
		}
	}
}

func TestFormatTraceparent(t *testing.T) {
	sc := SpanContext{TraceID: validTraceID, SpanID: validSpanID, Sampled: true}
	if got := FormatTraceparent(sc); got != validTP {
		t.Fatalf("FormatTraceparent = %q, want %q", got, validTP)
	}
	scUn := SpanContext{TraceID: validTraceID, SpanID: validSpanID, Sampled: false}
	want := "00-" + validTraceID + "-" + validSpanID + "-00"
	if got := FormatTraceparent(scUn); got != want {
		t.Fatalf("unsampled FormatTraceparent = %q, want %q", got, want)
	}
}

func TestTraceparent_RoundTrip(t *testing.T) {
	orig := SpanContext{TraceID: validTraceID, SpanID: validSpanID, Sampled: true}
	parsed, ok := ParseTraceparent(FormatTraceparent(orig))
	if !ok {
		t.Fatal("round-trip must parse")
	}
	if parsed.TraceID != orig.TraceID || parsed.SpanID != orig.SpanID || parsed.Sampled != orig.Sampled {
		t.Fatalf("round-trip mismatch: got %+v, want %+v", parsed, orig)
	}
}

func TestInjectExtractHTTP_RoundTrip(t *testing.T) {
	h := http.Header{}
	sc := SpanContext{TraceID: validTraceID, SpanID: validSpanID, Sampled: true}
	InjectHTTP(h, sc)

	if got := h.Get("Traceparent"); got != validTP {
		t.Fatalf("injected header = %q, want %q", got, validTP)
	}

	out, ok := ExtractHTTP(h)
	if !ok {
		t.Fatal("ExtractHTTP must read the injected header")
	}
	if out.TraceID != sc.TraceID || out.SpanID != sc.SpanID || out.Sampled != sc.Sampled {
		t.Fatalf("extracted %+v, want %+v", out, sc)
	}
}

func TestInjectHTTP_SkipsInvalid(t *testing.T) {
	h := http.Header{}
	InjectHTTP(h, SpanContext{TraceID: "short", SpanID: "x"})
	if h.Get("Traceparent") != "" {
		t.Fatal("InjectHTTP must not write an invalid SpanContext")
	}
}

func TestExtractHTTP_MissingHeader(t *testing.T) {
	if _, ok := ExtractHTTP(http.Header{}); ok {
		t.Fatal("ExtractHTTP on empty header must return ok=false")
	}
}

func TestExtractHTTP_MalformedHeader(t *testing.T) {
	h := http.Header{}
	h.Set("Traceparent", "garbage-value")
	if _, ok := ExtractHTTP(h); ok {
		t.Fatal("ExtractHTTP must return ok=false for a malformed header")
	}
}

func TestSpanContext_Valid(t *testing.T) {
	cases := []struct {
		name string
		sc   SpanContext
		want bool
	}{
		{"valid", SpanContext{TraceID: validTraceID, SpanID: validSpanID}, true},
		{"zero trace", SpanContext{TraceID: zeroTraceID, SpanID: validSpanID}, false},
		{"zero span", SpanContext{TraceID: validTraceID, SpanID: zeroSpanID}, false},
		{"short trace", SpanContext{TraceID: "abc", SpanID: validSpanID}, false},
		{"short span", SpanContext{TraceID: validTraceID, SpanID: "abc"}, false},
		{"empty", SpanContext{}, false},
	}
	for _, c := range cases {
		if got := c.sc.Valid(); got != c.want {
			t.Errorf("%s: Valid() = %v, want %v", c.name, got, c.want)
		}
	}
}
