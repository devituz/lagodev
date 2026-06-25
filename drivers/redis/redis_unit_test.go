package redis

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/devituz/lagodev/broadcasting"
	"github.com/devituz/lagodev/queue"
)

// These tests exercise the driver's pure logic — wire encoding, key
// building and option handling — without touching a Redis server, so
// they always run (including under -short and in CI without Redis).

// --- Broadcaster wire format --------------------------------------------

func TestEncodeDecodeEvent_Roundtrip(t *testing.T) {
	cases := []struct {
		name    string
		payload []byte
	}{
		{"MsgPosted", []byte(`{"x":1}`)},
		{"", []byte("no name")},
		{"OnlyName", nil},
		{"WithNewlineInPayload", []byte("line1\nline2")},
	}
	for _, c := range cases {
		t.Run(c.name+"|"+string(c.payload), func(t *testing.T) {
			frame := encodeEvent(broadcasting.Event{Name: c.name, Payload: c.payload})
			gotName, gotPayload := decodeEvent(frame)
			if gotName != c.name {
				t.Fatalf("name = %q, want %q", gotName, c.name)
			}
			if string(gotPayload) != string(c.payload) {
				t.Fatalf("payload = %q, want %q", gotPayload, c.payload)
			}
		})
	}
}

func TestEncodeEvent_Format(t *testing.T) {
	frame := encodeEvent(broadcasting.Event{Name: "Evt", Payload: []byte("body")})
	if frame != "Evt\nbody" {
		t.Fatalf("frame = %q, want %q", frame, "Evt\nbody")
	}
	// Empty name still produces a leading separator so decode recovers
	// an empty name rather than mistaking the payload for the name.
	frame = encodeEvent(broadcasting.Event{Name: "", Payload: []byte("body")})
	if !strings.HasPrefix(frame, "\n") {
		t.Fatalf("empty-name frame = %q, want leading newline", frame)
	}
}

func TestDecodeEvent_NoSeparator(t *testing.T) {
	// A frame with no newline is treated as a bare name with nil payload.
	name, payload := decodeEvent("BareName")
	if name != "BareName" || payload != nil {
		t.Fatalf("decode = (%q, %v), want (BareName, nil)", name, payload)
	}
}

// --- Broadcaster key building / options ---------------------------------

func TestBroadcaster_ChanKey(t *testing.T) {
	plain := NewBroadcaster(nil)
	if got := plain.chanKey("chat.1"); got != "chat.1" {
		t.Fatalf("no-prefix chanKey = %q, want chat.1", got)
	}
	prefixed := NewBroadcaster(nil, WithPrefix("app-a"))
	if got := prefixed.chanKey("chat.1"); got != "app-a:chat.1" {
		t.Fatalf("prefixed chanKey = %q, want app-a:chat.1", got)
	}
}

func TestBroadcaster_WithLogger(t *testing.T) {
	var called bool
	b := NewBroadcaster(nil, WithLogger(func(string, ...any) { called = true }))
	b.logger("boom")
	if !called {
		t.Fatal("WithLogger did not install the logger")
	}
}

func TestBroadcaster_DefaultsInitialised(t *testing.T) {
	b := NewBroadcaster(nil)
	if b.active == nil {
		t.Fatal("active map must be initialised")
	}
	if b.logger == nil {
		t.Fatal("default logger must be non-nil so it's always callable")
	}
	// Must not panic with no logger option set.
	b.logger("noop")
}

// --- Queue key building / options ---------------------------------------

func TestQueue_KeyBuilding(t *testing.T) {
	q := NewQueue(nil, "emails")
	if got := q.readyKey(); got != "lagodev:queue:emails" {
		t.Fatalf("readyKey = %q", got)
	}
	if got := q.delayedKey(); got != "lagodev:queue:emails:delayed" {
		t.Fatalf("delayedKey = %q", got)
	}
	if got := q.reservedKey(); got != "lagodev:queue:emails:reserved" {
		t.Fatalf("reservedKey = %q", got)
	}
}

func TestQueue_WithPrefix(t *testing.T) {
	q := NewQueue(nil, "emails", QueueWithPrefix("acme"))
	if got := q.readyKey(); got != "acme:queue:emails" {
		t.Fatalf("prefixed readyKey = %q", got)
	}
}

func TestQueue_OptionDefaults(t *testing.T) {
	q := NewQueue(nil, "default")
	if q.prefix != "lagodev" {
		t.Fatalf("default prefix = %q", q.prefix)
	}
	if q.visTout != 5*time.Minute {
		t.Fatalf("default visibility timeout = %v", q.visTout)
	}
	if q.popDelay != 100*time.Millisecond {
		t.Fatalf("default pop delay = %v", q.popDelay)
	}
}

func TestQueue_OptionsOverride(t *testing.T) {
	q := NewQueue(nil, "default",
		QueueWithVisibilityTimeout(time.Second),
		QueueWithPopPollInterval(10*time.Millisecond),
	)
	if q.visTout != time.Second {
		t.Fatalf("visTout = %v, want 1s", q.visTout)
	}
	if q.popDelay != 10*time.Millisecond {
		t.Fatalf("popDelay = %v, want 10ms", q.popDelay)
	}
}

// --- Job serialisation --------------------------------------------------

func TestJob_MarshalRoundtrip(t *testing.T) {
	in := queue.Job{
		ID:       "j-1",
		Name:     "Demo",
		Payload:  []byte(`{"x":1}`),
		Attempts: 2,
	}
	buf, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out queue.Job
	if err := json.Unmarshal(buf, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.ID != in.ID || out.Name != in.Name ||
		string(out.Payload) != string(in.Payload) || out.Attempts != in.Attempts {
		t.Fatalf("roundtrip mismatch: %+v != %+v", out, in)
	}
}
