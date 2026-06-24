// Package events provides an in-process event dispatcher modelled on
// Laravel's Event facade.
//
// Events are arbitrary values (typically structs). Listeners are
// functions that receive the dispatched event and an error. Dispatch
// is synchronous — listeners run in registration order on the calling
// goroutine. Background fan-out is left to the caller (spawn a
// goroutine inside a listener, or use a queue package).
//
// Type-safe registration uses Go generics so listeners don't need to
// type-assert:
//
//	type UserRegistered struct{ ID uint64; Email string }
//
//	d := events.New()
//	events.Listen(d, func(ctx context.Context, e UserRegistered) error {
//	    return sendWelcomeEmail(ctx, e.Email)
//	})
//	_ = d.Dispatch(ctx, UserRegistered{ID: 42, Email: "a@b.co"})
//
// If a listener returns an error, the dispatcher records it and (by
// default) continues with the remaining listeners. Use
// StopOnError(true) to short-circuit instead. Errors are joined with
// errors.Join so callers can inspect each one.
package events

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
)

// Dispatcher routes events to registered listeners.
type Dispatcher struct {
	mu       sync.RWMutex
	handlers map[reflect.Type][]rawListener
	stopOn   bool
}

// rawListener is the type-erased function the dispatcher stores; the
// generic Listen() wrapper hides the assertion from callers.
type rawListener struct {
	fn reflect.Value
}

// New returns a fresh Dispatcher.
func New() *Dispatcher {
	return &Dispatcher{handlers: make(map[reflect.Type][]rawListener)}
}

// StopOnError toggles short-circuit behaviour. Default false: every
// listener runs, errors are joined and returned together.
func (d *Dispatcher) StopOnError(v bool) *Dispatcher {
	d.mu.Lock()
	d.stopOn = v
	d.mu.Unlock()
	return d
}

// Listen registers fn as a listener for events of type E. Listeners
// run in registration order.
func Listen[E any](d *Dispatcher, fn func(ctx context.Context, e E) error) {
	var zero E
	t := reflect.TypeOf(zero)
	if t == nil {
		// E is an interface — fall back to the dynamic type from the
		// fn argument; not generally useful, so reject loudly.
		panic("events: cannot Listen on a nil interface type")
	}
	d.mu.Lock()
	d.handlers[t] = append(d.handlers[t], rawListener{fn: reflect.ValueOf(fn)})
	d.mu.Unlock()
}

// Dispatch fans the event out to its listeners. Returns the joined
// errors from listeners (or nil).
func (d *Dispatcher) Dispatch(ctx context.Context, event any) error {
	// Normalize pointer events to their element value so Dispatch(&Evt{})
	// reaches listeners registered via Listen[Evt]. Listeners always take
	// the value type (E in Listen[E]); without this, a pointer dispatch
	// would silently match nothing.
	ev := reflect.ValueOf(event)
	for ev.Kind() == reflect.Ptr {
		if ev.IsNil() {
			return nil
		}
		ev = ev.Elem()
	}
	t := ev.Type()
	d.mu.RLock()
	handlers := d.handlers[t]
	stopOn := d.stopOn
	d.mu.RUnlock()
	if len(handlers) == 0 {
		return nil
	}
	args := []reflect.Value{reflect.ValueOf(ctx), ev}
	var errs []error
	for _, h := range handlers {
		if err := callListener(h.fn, args); err != nil {
			errs = append(errs, err)
			if stopOn {
				break
			}
		}
	}
	return errors.Join(errs...)
}

// callListener invokes a listener and converts a panic into an error so
// one misbehaving listener cannot abort the rest of the fan-out.
func callListener(fn reflect.Value, args []reflect.Value) (err error) {
	defer func() {
		if r := recover(); r != nil {
			if e, ok := r.(error); ok {
				err = fmt.Errorf("events: listener panic: %w", e)
			} else {
				err = fmt.Errorf("events: listener panic: %v", r)
			}
		}
	}()
	out := fn.Call(args)
	if !out[0].IsNil() {
		err = out[0].Interface().(error)
	}
	return err
}

// HasListeners reports whether at least one listener is registered for
// events of type E. Handy for unit tests and conditional dispatch.
func HasListeners[E any](d *Dispatcher) bool {
	var zero E
	t := reflect.TypeOf(zero)
	d.mu.RLock()
	defer d.mu.RUnlock()
	return len(d.handlers[t]) > 0
}

// Forget removes every listener registered for events of type E. The
// returned bool is true if anything was removed.
func Forget[E any](d *Dispatcher) bool {
	var zero E
	t := reflect.TypeOf(zero)
	d.mu.Lock()
	defer d.mu.Unlock()
	_, ok := d.handlers[t]
	delete(d.handlers, t)
	return ok
}
