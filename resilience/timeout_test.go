package resilience

import (
	"context"
	"errors"
	"runtime"
	"sync"
	"testing"
	"time"
)

func TestTimeout_FastCallReturnsResult(t *testing.T) {
	to := NewTimeout(TimeoutConfig{Duration: time.Second})
	out, err := to.Execute(context.Background(), func(ctx context.Context) (any, error) {
		return "ok", nil
	})
	if err != nil || out != "ok" {
		t.Fatalf("Execute = (%v, %v)", out, err)
	}
}

func TestTimeout_ZeroDurationDisablesBound(t *testing.T) {
	to := NewTimeout(TimeoutConfig{Duration: 0})
	// fn blocks briefly; with no bound it still completes and is run inline.
	out, err := to.Execute(context.Background(), func(ctx context.Context) (any, error) {
		if ctx.Err() != nil {
			t.Error("ctx unexpectedly bounded")
		}
		return 7, nil
	})
	if err != nil || out != 7 {
		t.Fatalf("Execute = (%v, %v)", out, err)
	}
}

func TestTimeout_ExceededReturnsErrTimeout(t *testing.T) {
	to := NewTimeout(TimeoutConfig{Duration: 50 * time.Millisecond})
	release := make(chan struct{})
	defer close(release)
	_, err := to.Execute(context.Background(), func(ctx context.Context) (any, error) {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-release:
			return "late", nil
		}
	})
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("err = %v, want ErrTimeout", err)
	}
}

func TestTimeout_DerivedContextCancelledOnTimeout(t *testing.T) {
	to := NewTimeout(TimeoutConfig{Duration: 30 * time.Millisecond})
	observed := make(chan error, 1)
	_, err := to.Execute(context.Background(), func(ctx context.Context) (any, error) {
		<-ctx.Done()
		observed <- ctx.Err()
		return nil, ctx.Err()
	})
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("err = %v, want ErrTimeout", err)
	}
	select {
	case e := <-observed:
		if !errors.Is(e, context.DeadlineExceeded) {
			t.Fatalf("fn saw ctx.Err() = %v, want DeadlineExceeded", e)
		}
	case <-time.After(time.Second):
		t.Fatal("fn never observed cancellation")
	}
}

func TestTimeout_ParentCancellationPropagates(t *testing.T) {
	to := NewTimeout(TimeoutConfig{Duration: time.Hour})
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	_, err := to.Execute(ctx, func(ctx context.Context) (any, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	})
	// Parent cancellation surfaces as context.Canceled, not ErrTimeout.
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if errors.Is(err, ErrTimeout) {
		t.Fatal("parent cancel misreported as ErrTimeout")
	}
}

func TestTimeout_NoGoroutineLeak(t *testing.T) {
	to := NewTimeout(TimeoutConfig{Duration: 10 * time.Millisecond})
	before := runtime.NumGoroutine()
	for i := 0; i < 200; i++ {
		_, _ = to.Execute(context.Background(), func(ctx context.Context) (any, error) {
			// Ignores cancellation for a bit, then exits — the buffered
			// channel lets it send without blocking even after Execute returned.
			time.Sleep(25 * time.Millisecond)
			return "late", nil
		})
	}
	// Let the slow goroutines drain.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		runtime.GC()
		if runtime.NumGoroutine() <= before+5 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("goroutines leaked: before=%d after=%d", before, runtime.NumGoroutine())
}

func TestTimeout_ComposesWithWrap(t *testing.T) {
	to := NewTimeout(TimeoutConfig{Duration: 50 * time.Millisecond})
	guard := Wrap(to.Middleware())
	_, err := guard(func(ctx context.Context) (any, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	})(context.Background())
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("err = %v, want ErrTimeout", err)
	}
}

func TestTimeout_ConcurrentRace(t *testing.T) {
	to := NewTimeout(TimeoutConfig{Duration: 5 * time.Millisecond})
	var wg sync.WaitGroup
	for g := 0; g < 50; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				_, _ = to.Execute(context.Background(), func(ctx context.Context) (any, error) {
					if (g+i)%2 == 0 {
						return "ok", nil
					}
					<-ctx.Done()
					return nil, ctx.Err()
				})
			}
		}(g)
	}
	wg.Wait()
}
