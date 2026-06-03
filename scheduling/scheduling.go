// Package scheduling provides a task scheduler modelled on Laravel's
// Task Scheduling. Tasks run on a wall-clock cadence (every X minutes,
// every hour, every day at a fixed time, …) and are dispatched by a
// single in-process Runner.
//
// No external cron parser is required — Laravel's most common
// expressions (EveryMinute, EveryFiveMinutes, Hourly, Daily, Weekly,
// Monthly, At "HH:MM", Cron expression) are expressed as small Schedule
// values that the Runner ticks against once per second.
//
// Usage:
//
//	s := scheduling.New()
//	s.Job("cleanup-old-sessions", scheduling.Hourly(), func(ctx context.Context) error {
//	    return svc.PurgeStaleSessions(ctx)
//	})
//	s.Job("nightly-billing", scheduling.DailyAt("02:00"), runBilling)
//	s.Job("metrics-flush", scheduling.Every(5*time.Minute), flushMetrics)
//	go s.Run(ctx)
package scheduling

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Schedule decides whether a job is due at instant t. Implementations
// are stateless — the Runner remembers the last tick.
type Schedule interface {
	Due(prev, now time.Time) bool
	String() string
}

// scheduleFunc is an inline Schedule for one-off custom rules.
type scheduleFunc struct {
	due  func(prev, now time.Time) bool
	desc string
}

func (s scheduleFunc) Due(prev, now time.Time) bool { return s.due(prev, now) }
func (s scheduleFunc) String() string               { return s.desc }

// Every returns a Schedule that fires every interval starting from
// the moment the Runner first sees the job.
func Every(interval time.Duration) Schedule {
	if interval <= 0 {
		panic("scheduling: Every interval must be > 0")
	}
	return scheduleFunc{
		desc: "every " + interval.String(),
		due: func(prev, now time.Time) bool {
			return now.Sub(prev) >= interval
		},
	}
}

// EveryMinute is a convenience alias.
func EveryMinute() Schedule { return Every(time.Minute) }

// EveryFiveMinutes is a convenience alias.
func EveryFiveMinutes() Schedule { return Every(5 * time.Minute) }

// Hourly fires once per hour at the top of the hour.
func Hourly() Schedule {
	return scheduleFunc{
		desc: "hourly",
		due: func(prev, now time.Time) bool {
			return now.Hour() != prev.Hour() || now.Day() != prev.Day()
		},
	}
}

// Daily fires once per day at 00:00 local time.
func Daily() Schedule { return DailyAt("00:00") }

// DailyAt fires once per day at the given "HH:MM" (local time).
func DailyAt(hhmm string) Schedule {
	h, m, err := parseHHMM(hhmm)
	if err != nil {
		panic(err)
	}
	return scheduleFunc{
		desc: "daily at " + hhmm,
		due: func(prev, now time.Time) bool {
			// Fire when the current minute matches and we haven't
			// already fired today.
			if now.Hour() != h || now.Minute() != m {
				return false
			}
			return prev.Day() != now.Day() || prev.Hour() != now.Hour() || prev.Minute() != now.Minute()
		},
	}
}

// Weekly fires once per week on Monday at 00:00.
func Weekly() Schedule {
	return scheduleFunc{
		desc: "weekly on Monday",
		due: func(prev, now time.Time) bool {
			if now.Weekday() != time.Monday {
				return false
			}
			if now.Hour() != 0 || now.Minute() != 0 {
				return false
			}
			return prev.Day() != now.Day()
		},
	}
}

// Monthly fires once per month on the 1st at 00:00.
func Monthly() Schedule {
	return scheduleFunc{
		desc: "monthly on the 1st",
		due: func(prev, now time.Time) bool {
			if now.Day() != 1 || now.Hour() != 0 || now.Minute() != 0 {
				return false
			}
			return prev.Month() != now.Month()
		},
	}
}

func parseHHMM(s string) (int, int, error) {
	parts := strings.Split(s, ":")
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("scheduling: invalid HH:MM %q", s)
	}
	h, err := strconv.Atoi(parts[0])
	if err != nil || h < 0 || h > 23 {
		return 0, 0, fmt.Errorf("scheduling: invalid hour in %q", s)
	}
	m, err := strconv.Atoi(parts[1])
	if err != nil || m < 0 || m > 59 {
		return 0, 0, fmt.Errorf("scheduling: invalid minute in %q", s)
	}
	return h, m, nil
}

// TaskFn is the work performed when a schedule fires.
type TaskFn func(ctx context.Context) error

// task is the registered unit; the lastRun field is mutated by the
// Runner.
type task struct {
	name     string
	schedule Schedule
	fn       TaskFn

	mu      sync.Mutex
	lastRun time.Time
	running bool
}

// Runner orchestrates registered tasks.
type Runner struct {
	mu     sync.Mutex
	tasks  []*task
	tick   time.Duration
	logger func(string, ...any)
	now    func() time.Time
	stop   chan struct{}
}

// New returns a Runner ticking once per second.
func New() *Runner {
	return &Runner{
		tick:   time.Second,
		logger: func(string, ...any) {},
		now:    time.Now,
		stop:   make(chan struct{}),
	}
}

// Tick overrides the polling interval (useful for tests).
func (r *Runner) Tick(d time.Duration) *Runner { r.tick = d; return r }

// Logger installs a structured logger callback.
func (r *Runner) Logger(fn func(string, ...any)) *Runner { r.logger = fn; return r }

// Job registers a task. Duplicate names panic to catch typos early.
func (r *Runner) Job(name string, schedule Schedule, fn TaskFn) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, t := range r.tasks {
		if t.name == name {
			panic("scheduling: duplicate job name " + name)
		}
	}
	r.tasks = append(r.tasks, &task{
		name:     name,
		schedule: schedule,
		fn:       fn,
		// Seed lastRun so Every(x) doesn't fire immediately at startup.
		lastRun: r.now(),
	})
}

// Run blocks until ctx is cancelled or Stop is called.
func (r *Runner) Run(ctx context.Context) error {
	ticker := time.NewTicker(r.tick)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-r.stop:
			return nil
		case now := <-ticker.C:
			r.dispatch(ctx, now)
		}
	}
}

// Stop signals Run to exit.
func (r *Runner) Stop() {
	select {
	case <-r.stop:
	default:
		close(r.stop)
	}
}

func (r *Runner) dispatch(ctx context.Context, now time.Time) {
	r.mu.Lock()
	tasks := append([]*task(nil), r.tasks...)
	r.mu.Unlock()
	for _, t := range tasks {
		t.mu.Lock()
		due := t.schedule.Due(t.lastRun, now)
		alreadyRunning := t.running
		if due && !alreadyRunning {
			t.lastRun = now
			t.running = true
		}
		t.mu.Unlock()
		if !due || alreadyRunning {
			continue
		}
		go r.fire(ctx, t, now)
	}
}

func (r *Runner) fire(ctx context.Context, t *task, now time.Time) {
	defer func() {
		t.mu.Lock()
		t.running = false
		t.mu.Unlock()
	}()
	if err := t.fn(ctx); err != nil {
		r.logger("scheduling: %s failed: %v", t.name, err)
	}
}

// Tasks returns metadata for each registered task. Useful for
// management endpoints / status pages.
func (r *Runner) Tasks() []TaskInfo {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]TaskInfo, 0, len(r.tasks))
	for _, t := range r.tasks {
		t.mu.Lock()
		out = append(out, TaskInfo{
			Name:     t.name,
			Schedule: t.schedule.String(),
			LastRun:  t.lastRun,
			Running:  t.running,
		})
		t.mu.Unlock()
	}
	return out
}

// TaskInfo is a snapshot of a registered task.
type TaskInfo struct {
	Name     string
	Schedule string
	LastRun  time.Time
	Running  bool
}

// ErrAlreadyRegistered is reserved for drivers that detect duplicate
// task names at register-time when the panic-on-collision behaviour
// is unsuitable.
var ErrAlreadyRegistered = errors.New("scheduling: name already registered")
