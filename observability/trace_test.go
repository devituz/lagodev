package observability

import (
	"context"
	"errors"
	"sync"
	"testing"
)

func TestSpanStatus_String(t *testing.T) {
	cases := map[SpanStatus]string{
		StatusUnset:    "unset",
		StatusOK:       "ok",
		StatusError:    "error",
		SpanStatus(99): "unset",
	}
	for s, want := range cases {
		if got := s.String(); got != want {
			t.Fatalf("SpanStatus(%d).String() = %q, want %q", int(s), got, want)
		}
	}
}

func TestTracer_StartRootSpan(t *testing.T) {
	tr := NewTracer()
	ctx, span := tr.Start(context.Background(), "root")
	defer span.End()

	sc := span.Context()
	if !sc.Valid() {
		t.Fatalf("root span context invalid: %+v", sc)
	}
	if sc.ParentID != "" {
		t.Fatalf("root span ParentID = %q, want empty", sc.ParentID)
	}
	if !sc.Sampled {
		t.Fatal("root span should be sampled by default")
	}
	// The returned context must carry the new span context.
	got, ok := SpanContextFromContext(ctx)
	if !ok || got.SpanID != sc.SpanID {
		t.Fatalf("returned ctx must carry span context: ok=%v got=%+v", ok, got)
	}
}

func TestTracer_ChildInheritsTrace(t *testing.T) {
	tr := NewTracer()
	ctx, parent := tr.Start(context.Background(), "parent")
	defer parent.End()

	_, child := tr.Start(ctx, "child")
	defer child.End()

	p := parent.Context()
	c := child.Context()

	if c.TraceID != p.TraceID {
		t.Fatalf("child TraceID = %q, want parent's %q", c.TraceID, p.TraceID)
	}
	if c.ParentID != p.SpanID {
		t.Fatalf("child ParentID = %q, want parent SpanID %q", c.ParentID, p.SpanID)
	}
	if c.SpanID == p.SpanID {
		t.Fatal("child must have a distinct SpanID from parent")
	}
	if c.Sampled != p.Sampled {
		t.Fatal("child must inherit parent's Sampled flag")
	}
}

func TestTracer_DistinctTraceIDsForRoots(t *testing.T) {
	tr := NewTracer()
	_, a := tr.Start(context.Background(), "a")
	_, b := tr.Start(context.Background(), "b")
	a.End()
	b.End()
	if a.Context().TraceID == b.Context().TraceID {
		t.Fatal("two independent root spans must get distinct trace ids")
	}
}

func TestTracer_StartIgnoresInvalidParent(t *testing.T) {
	tr := NewTracer()
	// Seed an invalid (zero) span context; Start must treat it as a new root.
	ctx := ContextWithSpanContext(context.Background(), SpanContext{
		TraceID: zeroTraceID,
		SpanID:  zeroSpanID,
	})
	_, span := tr.Start(ctx, "op")
	defer span.End()
	sc := span.Context()
	if sc.TraceID == zeroTraceID {
		t.Fatal("invalid parent must be ignored; a fresh trace id should be generated")
	}
	if sc.ParentID != "" {
		t.Fatalf("invalid parent must yield empty ParentID, got %q", sc.ParentID)
	}
}

func TestSpan_AttributesAndStatusRecorded(t *testing.T) {
	tr := NewTracer()
	rec, ok := tr.(Recorder)
	if !ok {
		t.Fatal("default tracer must implement Recorder")
	}

	_, span := tr.Start(context.Background(), "work")
	span.SetAttr("user_id", 42).SetAttr("path", "/x")
	span.SetStatus(StatusOK, "done")
	span.End()

	spans := rec.Spans()
	if len(spans) != 1 {
		t.Fatalf("recorder has %d spans, want 1", len(spans))
	}
	r := spans[0]
	if r.Name != "work" {
		t.Fatalf("recorded Name = %q, want work", r.Name)
	}
	if r.Status != StatusOK || r.StatusMsg != "done" {
		t.Fatalf("recorded status = %v/%q, want ok/done", r.Status, r.StatusMsg)
	}
	if r.Attrs["user_id"] != 42 || r.Attrs["path"] != "/x" {
		t.Fatalf("recorded Attrs = %+v", r.Attrs)
	}
	if r.Duration < 0 {
		t.Fatalf("recorded Duration negative: %v", r.Duration)
	}
}

func TestSpan_RecordError(t *testing.T) {
	tr := NewTracer()
	rec := tr.(Recorder)

	_, span := tr.Start(context.Background(), "fail")
	span.RecordError(errors.New("boom"))
	span.End()

	r := rec.Spans()[0]
	if r.Err != "boom" {
		t.Fatalf("recorded Err = %q, want boom", r.Err)
	}
	if r.Status != StatusError {
		t.Fatalf("RecordError on unset status must set StatusError, got %v", r.Status)
	}
}

func TestSpan_RecordErrorNilNoop(t *testing.T) {
	tr := NewTracer()
	rec := tr.(Recorder)
	_, span := tr.Start(context.Background(), "ok")
	span.RecordError(nil)
	span.End()
	r := rec.Spans()[0]
	if r.Err != "" || r.Status != StatusUnset {
		t.Fatalf("RecordError(nil) must be a no-op, got Err=%q Status=%v", r.Err, r.Status)
	}
}

func TestSpan_RecordErrorDoesNotOverrideExplicitStatus(t *testing.T) {
	tr := NewTracer()
	rec := tr.(Recorder)
	_, span := tr.Start(context.Background(), "x")
	span.SetStatus(StatusOK, "fine")
	span.RecordError(errors.New("late"))
	span.End()
	r := rec.Spans()[0]
	if r.Status != StatusOK {
		t.Fatalf("RecordError must not override an explicit status; got %v", r.Status)
	}
	if r.Err != "late" {
		t.Fatalf("error message should still be recorded, got %q", r.Err)
	}
}

func TestSpan_EndIdempotent(t *testing.T) {
	tr := NewTracer()
	rec := tr.(Recorder)
	_, span := tr.Start(context.Background(), "once")
	span.End()
	span.End() // second End must not record a duplicate
	if got := len(rec.Spans()); got != 1 {
		t.Fatalf("double End recorded %d spans, want 1", got)
	}
}

func TestSpan_AttrsSnapshotIsolatedFromLiveSpan(t *testing.T) {
	tr := NewTracer()
	rec := tr.(Recorder)
	_, span := tr.Start(context.Background(), "iso")
	span.SetAttr("k", "v")
	span.End()

	// End() copies the live attrs map into the record, so a later mutation of
	// the span's attributes must not be visible in the already-recorded span.
	span.SetAttr("k", "after-end")

	r := rec.Spans()[0]
	if r.Attrs["k"] != "v" {
		t.Fatalf("recorded Attrs must be a snapshot taken at End(); got %q", r.Attrs["k"])
	}
}

func TestRecorder_RingBufferOverwrite(t *testing.T) {
	tr := NewTracer(WithRingCapacity(3))
	rec := tr.(Recorder)

	for i := 0; i < 5; i++ {
		_, span := tr.Start(context.Background(), string(rune('a'+i)))
		span.End()
	}
	spans := rec.Spans()
	if len(spans) != 3 {
		t.Fatalf("ring of cap 3 holds %d spans, want 3", len(spans))
	}
	// Oldest-first; the 2 oldest (a,b) were overwritten -> c,d,e remain.
	want := []string{"c", "d", "e"}
	for i, w := range want {
		if spans[i].Name != w {
			t.Fatalf("ring snapshot[%d] = %q, want %q (oldest-first)", i, spans[i].Name, w)
		}
	}
}

func TestRecorder_EmptyRing(t *testing.T) {
	tr := NewTracer()
	if got := tr.(Recorder).Spans(); len(got) != 0 {
		t.Fatalf("fresh tracer Spans len = %d, want 0", len(got))
	}
}

// TestConcurrentSpans is meaningful under -race: parallel span creation,
// mutation, end, and snapshot reads against the shared ring buffer.
func TestConcurrentSpans(t *testing.T) {
	tr := NewTracer(WithRingCapacity(64))
	rec := tr.(Recorder)

	var wg sync.WaitGroup
	const n = 40
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			ctx, parent := tr.Start(context.Background(), "p")
			parent.SetAttr("g", 1)
			_, child := tr.Start(ctx, "c")
			child.RecordError(errors.New("e"))
			child.End()
			parent.SetStatus(StatusOK, "")
			parent.End()
			_ = rec.Spans() // concurrent read of the ring
		}()
	}
	wg.Wait()
}
