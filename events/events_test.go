package events

import (
	"context"
	"errors"
	"testing"
)

type UserRegistered struct {
	ID    uint64
	Email string
}

type OrderPlaced struct {
	OrderID uint64
}

func TestDispatch_NoListeners_ReturnsNil(t *testing.T) {
	d := New()
	if err := d.Dispatch(context.Background(), UserRegistered{}); err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
}

func TestListenAndDispatch_TypedPayload(t *testing.T) {
	d := New()
	var got UserRegistered
	Listen(d, func(_ context.Context, e UserRegistered) error {
		got = e
		return nil
	})
	want := UserRegistered{ID: 7, Email: "a@b.co"}
	if err := d.Dispatch(context.Background(), want); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if got != want {
		t.Fatalf("listener got %+v, want %+v", got, want)
	}
}

func TestListen_MultipleListenersFireInOrder(t *testing.T) {
	d := New()
	var order []int
	Listen(d, func(_ context.Context, _ UserRegistered) error { order = append(order, 1); return nil })
	Listen(d, func(_ context.Context, _ UserRegistered) error { order = append(order, 2); return nil })
	Listen(d, func(_ context.Context, _ UserRegistered) error { order = append(order, 3); return nil })
	_ = d.Dispatch(context.Background(), UserRegistered{})
	if len(order) != 3 || order[0] != 1 || order[1] != 2 || order[2] != 3 {
		t.Fatalf("listeners did not fire in order, got %v", order)
	}
}

func TestDispatch_DifferentTypesIsolated(t *testing.T) {
	d := New()
	urHits, opHits := 0, 0
	Listen(d, func(_ context.Context, _ UserRegistered) error { urHits++; return nil })
	Listen(d, func(_ context.Context, _ OrderPlaced) error { opHits++; return nil })
	_ = d.Dispatch(context.Background(), UserRegistered{})
	_ = d.Dispatch(context.Background(), OrderPlaced{})
	_ = d.Dispatch(context.Background(), OrderPlaced{})
	if urHits != 1 || opHits != 2 {
		t.Fatalf("hits = (%d, %d); want (1, 2)", urHits, opHits)
	}
}

func TestDispatch_CollectsErrors(t *testing.T) {
	d := New()
	a := errors.New("a")
	b := errors.New("b")
	Listen(d, func(_ context.Context, _ UserRegistered) error { return a })
	Listen(d, func(_ context.Context, _ UserRegistered) error { return nil })
	Listen(d, func(_ context.Context, _ UserRegistered) error { return b })
	err := d.Dispatch(context.Background(), UserRegistered{})
	if !errors.Is(err, a) || !errors.Is(err, b) {
		t.Fatalf("joined err must wrap both a and b, got %v", err)
	}
}

func TestDispatch_StopOnError(t *testing.T) {
	d := New().StopOnError(true)
	a := errors.New("first")
	hit2 := false
	Listen(d, func(_ context.Context, _ UserRegistered) error { return a })
	Listen(d, func(_ context.Context, _ UserRegistered) error { hit2 = true; return nil })
	err := d.Dispatch(context.Background(), UserRegistered{})
	if !errors.Is(err, a) {
		t.Fatalf("want first err, got %v", err)
	}
	if hit2 {
		t.Fatal("second listener must not run when StopOnError=true")
	}
}

func TestHasListeners(t *testing.T) {
	d := New()
	if HasListeners[UserRegistered](d) {
		t.Fatal("none registered yet")
	}
	Listen(d, func(_ context.Context, _ UserRegistered) error { return nil })
	if !HasListeners[UserRegistered](d) {
		t.Fatal("must be true after Listen")
	}
	if HasListeners[OrderPlaced](d) {
		t.Fatal("isolated per type")
	}
}

func TestForget(t *testing.T) {
	d := New()
	Listen(d, func(_ context.Context, _ UserRegistered) error { return nil })
	if !Forget[UserRegistered](d) {
		t.Fatal("Forget must report removal")
	}
	if HasListeners[UserRegistered](d) {
		t.Fatal("listener still there after Forget")
	}
	if Forget[UserRegistered](d) {
		t.Fatal("second Forget must report no-op")
	}
}

// TestDispatch_PanicInListenerIsContained ensures a panicking listener
// does not abort the remaining listeners and surfaces as a joined error.
func TestDispatch_PanicInListenerIsContained(t *testing.T) {
	d := New()
	secondRan := false
	Listen(d, func(_ context.Context, _ UserRegistered) error { panic("boom") })
	Listen(d, func(_ context.Context, _ UserRegistered) error { secondRan = true; return nil })

	err := d.Dispatch(context.Background(), UserRegistered{})
	if err == nil {
		t.Fatal("panic must be reported as an error")
	}
	if !secondRan {
		t.Fatal("second listener must still run after the first panics")
	}
}

// TestDispatch_PanicRespectsStopOnError ensures a panicking listener
// short-circuits when StopOnError is set.
func TestDispatch_PanicRespectsStopOnError(t *testing.T) {
	d := New().StopOnError(true)
	secondRan := false
	Listen(d, func(_ context.Context, _ UserRegistered) error { panic("boom") })
	Listen(d, func(_ context.Context, _ UserRegistered) error { secondRan = true; return nil })

	if err := d.Dispatch(context.Background(), UserRegistered{}); err == nil {
		t.Fatal("panic must be reported as an error")
	}
	if secondRan {
		t.Fatal("StopOnError must halt after a panicking listener")
	}
}

// TestDispatch_PointerEventReachesValueListener verifies Dispatch(&Evt{})
// reaches a listener registered with Listen[Evt].
func TestDispatch_PointerEventReachesValueListener(t *testing.T) {
	d := New()
	var got UserRegistered
	hit := false
	Listen(d, func(_ context.Context, e UserRegistered) error {
		got = e
		hit = true
		return nil
	})
	want := UserRegistered{ID: 11, Email: "p@q.co"}
	if err := d.Dispatch(context.Background(), &want); err != nil {
		t.Fatalf("Dispatch(&evt): %v", err)
	}
	if !hit {
		t.Fatal("pointer dispatch missed the value listener")
	}
	if got != want {
		t.Fatalf("listener got %+v, want %+v", got, want)
	}
}

func TestContextPropagation(t *testing.T) {
	d := New()
	type ctxKey string
	want := "abc"
	var got string
	Listen(d, func(ctx context.Context, _ UserRegistered) error {
		got, _ = ctx.Value(ctxKey("trace")).(string)
		return nil
	})
	ctx := context.WithValue(context.Background(), ctxKey("trace"), want)
	_ = d.Dispatch(ctx, UserRegistered{})
	if got != want {
		t.Fatalf("ctx value didn't reach listener; got %q", got)
	}
}
