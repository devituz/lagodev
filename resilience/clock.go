package resilience

import (
	"sort"
	"sync"
	"time"
)

// Clock abstracts the parts of the time package the primitives depend on so
// timing behaviour can be driven deterministically in tests. The production
// implementation (realClock) delegates to the standard library.
type Clock interface {
	// Now returns the current time.
	Now() time.Time
	// After returns a channel that receives once d has elapsed.
	After(d time.Duration) <-chan time.Time
	// Sleep blocks for d (or until the channel from After would fire).
	Sleep(d time.Duration)
}

// realClock is the default Clock backed by the time package.
type realClock struct{}

func (realClock) Now() time.Time                         { return time.Now() }
func (realClock) After(d time.Duration) <-chan time.Time { return time.After(d) }
func (realClock) Sleep(d time.Duration)                  { time.Sleep(d) }

// SystemClock is the production Clock used when no clock is injected.
var SystemClock Clock = realClock{}

// FakeClock is a manually advanced Clock for deterministic tests. Time only
// moves when Advance is called; timers registered via After fire when the
// virtual clock passes their deadline. It is goroutine-safe.
type FakeClock struct {
	mu      sync.Mutex
	now     time.Time
	waiters []fakeWaiter
}

type fakeWaiter struct {
	deadline time.Time
	ch       chan time.Time
}

// NewFakeClock returns a FakeClock anchored at start.
func NewFakeClock(start time.Time) *FakeClock {
	return &FakeClock{now: start}
}

// Now returns the current virtual time.
func (c *FakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// After registers a timer. A non-positive d fires immediately.
func (c *FakeClock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	ch := make(chan time.Time, 1)
	deadline := c.now.Add(d)
	if d <= 0 {
		ch <- c.now
		return ch
	}
	c.waiters = append(c.waiters, fakeWaiter{deadline: deadline, ch: ch})
	return ch
}

// Sleep blocks until the virtual clock advances past d. It is implemented on
// top of After so a blocked Sleep is released by Advance from another
// goroutine.
func (c *FakeClock) Sleep(d time.Duration) {
	if d <= 0 {
		return
	}
	<-c.After(d)
}

// Advance moves the virtual clock forward by d and fires every timer whose
// deadline has passed.
func (c *FakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	now := c.now
	var fire []fakeWaiter
	kept := c.waiters[:0]
	for _, w := range c.waiters {
		if !w.deadline.After(now) {
			fire = append(fire, w)
		} else {
			kept = append(kept, w)
		}
	}
	// Detach the kept slice so we don't alias the backing array of fire.
	c.waiters = append([]fakeWaiter(nil), kept...)
	c.mu.Unlock()

	// Fire in deadline order for predictable wakeups.
	sort.Slice(fire, func(i, j int) bool { return fire[i].deadline.Before(fire[j].deadline) })
	for _, w := range fire {
		w.ch <- now
	}
}

// BlockedSleepers returns how many timers are currently pending. Tests use it
// to wait until a goroutine has parked on After/Sleep before advancing.
func (c *FakeClock) BlockedSleepers() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.waiters)
}
