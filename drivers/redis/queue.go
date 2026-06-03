package redis

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/devituz/lagodev/queue"
	redislib "github.com/redis/go-redis/v9"
)

// Queue implements queue.Queue against Redis.
//
// Two Redis keys are used per logical queue:
//   - "<prefix>:queue:<name>"           — LIST of ready jobs (LPUSH/BRPOPLPUSH)
//   - "<prefix>:queue:<name>:reserved"  — sorted set of reserved jobs
//                                         scored by their visibility-
//                                         timeout deadline; reaper
//                                         requeues entries past their
//                                         deadline.
//   - "<prefix>:queue:<name>:delayed"   — sorted set of delayed jobs
//                                         scored by their available_at
//                                         epoch.
//
// Promotion (delayed → ready and reserved-timeout → ready) is handled
// inline on each Pop so no background worker is required.
type Queue struct {
	client   *redislib.Client
	name     string
	prefix   string
	visTout  time.Duration
	popDelay time.Duration
	id       uint64
}

// QueueOption customises Queue behaviour.
type QueueOption func(*Queue)

// QueueWithPrefix overrides the key prefix (default "lagodev").
func QueueWithPrefix(p string) QueueOption {
	return func(q *Queue) { q.prefix = p }
}

// QueueWithVisibilityTimeout sets how long a reserved job stays
// invisible. Default 5 minutes.
func QueueWithVisibilityTimeout(d time.Duration) QueueOption {
	return func(q *Queue) { q.visTout = d }
}

// QueueWithPopPollInterval controls how often Pop polls for new work
// (between BRPOPLPUSH timeouts). Default 100ms — lower values cut
// latency at the cost of Redis CPU.
func QueueWithPopPollInterval(d time.Duration) QueueOption {
	return func(q *Queue) { q.popDelay = d }
}

// NewQueue returns a Queue named name backed by rdb.
func NewQueue(rdb *redislib.Client, name string, opts ...QueueOption) *Queue {
	q := &Queue{
		client:   rdb,
		name:     name,
		prefix:   "lagodev",
		visTout:  5 * time.Minute,
		popDelay: 100 * time.Millisecond,
	}
	for _, o := range opts {
		o(q)
	}
	return q
}

// Static compile-time guard.
var _ queue.Queue = (*Queue)(nil)

func (q *Queue) readyKey() string    { return q.prefix + ":queue:" + q.name }
func (q *Queue) delayedKey() string  { return q.prefix + ":queue:" + q.name + ":delayed" }
func (q *Queue) reservedKey() string { return q.prefix + ":queue:" + q.name + ":reserved" }

// Push enqueues j.
func (q *Queue) Push(ctx context.Context, j queue.Job) error {
	if j.ID == "" {
		j.ID = fmt.Sprintf("job-%d-%d", time.Now().UnixNano(), atomic.AddUint64(&q.id, 1))
	}
	buf, err := json.Marshal(j)
	if err != nil {
		return fmt.Errorf("redis-queue: marshal: %w", err)
	}
	if !j.AvailableAt.IsZero() && j.AvailableAt.After(time.Now()) {
		// delayed sorted set scored by deadline
		return q.client.ZAdd(ctx, q.delayedKey(), redislib.Z{
			Score:  float64(j.AvailableAt.UnixNano()),
			Member: buf,
		}).Err()
	}
	return q.client.LPush(ctx, q.readyKey(), buf).Err()
}

// Pop polls Redis until a job is available or wait elapses, returning
// queue.ErrEmpty in the latter case.
func (q *Queue) Pop(ctx context.Context, wait time.Duration) (queue.Job, error) {
	deadline := time.Now().Add(wait)
	for {
		// Promote due delayed jobs and timed-out reservations.
		q.promote(ctx)

		// Try a non-blocking RPOPLPUSH-style pop.
		buf, err := q.client.RPop(ctx, q.readyKey()).Bytes()
		if err == nil {
			return q.reserve(ctx, buf)
		}
		if !errors.Is(err, redislib.Nil) {
			return queue.Job{}, fmt.Errorf("redis-queue: rpop: %w", err)
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return queue.Job{}, queue.ErrEmpty
		}
		sleep := q.popDelay
		if remaining < sleep {
			sleep = remaining
		}
		select {
		case <-ctx.Done():
			return queue.Job{}, ctx.Err()
		case <-time.After(sleep):
		}
	}
}

// reserve marks the popped job as in-flight in the reserved zset and
// returns it.
func (q *Queue) reserve(ctx context.Context, buf []byte) (queue.Job, error) {
	var j queue.Job
	if err := json.Unmarshal(buf, &j); err != nil {
		return queue.Job{}, fmt.Errorf("redis-queue: unmarshal: %w", err)
	}
	deadline := time.Now().Add(q.visTout).UnixNano()
	if err := q.client.ZAdd(ctx, q.reservedKey(), redislib.Z{
		Score:  float64(deadline),
		Member: buf,
	}).Err(); err != nil {
		return queue.Job{}, fmt.Errorf("redis-queue: zadd reserved: %w", err)
	}
	return j, nil
}

// promote moves due delayed jobs back to the ready list and re-queues
// reserved jobs past their visibility deadline.
func (q *Queue) promote(ctx context.Context) {
	now := float64(time.Now().UnixNano())
	for _, key := range []string{q.delayedKey(), q.reservedKey()} {
		// Pull every member scored <= now.
		members, err := q.client.ZRangeByScoreWithScores(ctx, key, &redislib.ZRangeBy{
			Min: "-inf",
			Max: fmt.Sprintf("%f", now),
		}).Result()
		if err != nil {
			return
		}
		for _, m := range members {
			s, ok := m.Member.(string)
			if !ok {
				continue
			}
			// Remove from sorted set; push back to ready list.
			if removed, err := q.client.ZRem(ctx, key, s).Result(); err == nil && removed > 0 {
				_ = q.client.LPush(ctx, q.readyKey(), s).Err()
			}
		}
	}
}

// Ack removes the job from the reserved set. Linear scan is fine —
// reserved sets are small at any given moment (only in-flight jobs).
func (q *Queue) Ack(ctx context.Context, jobID string) error {
	return q.removeReserved(ctx, jobID, nil)
}

// Nack returns the job to the queue. If retryAfter > 0, it's stored
// in the delayed zset; otherwise pushed back on the ready list.
// Attempts is incremented.
func (q *Queue) Nack(ctx context.Context, jobID string, retryAfter time.Duration) error {
	return q.removeReserved(ctx, jobID, func(j queue.Job) error {
		j.Attempts++
		buf, err := json.Marshal(j)
		if err != nil {
			return err
		}
		if retryAfter > 0 {
			return q.client.ZAdd(ctx, q.delayedKey(), redislib.Z{
				Score:  float64(time.Now().Add(retryAfter).UnixNano()),
				Member: buf,
			}).Err()
		}
		return q.client.LPush(ctx, q.readyKey(), buf).Err()
	})
}

// removeReserved finds the reserved entry whose decoded ID matches
// jobID and removes it; if onFound is non-nil it's called with the
// decoded job before deletion.
func (q *Queue) removeReserved(ctx context.Context, jobID string, onFound func(queue.Job) error) error {
	members, err := q.client.ZRange(ctx, q.reservedKey(), 0, -1).Result()
	if err != nil {
		return fmt.Errorf("redis-queue: zrange reserved: %w", err)
	}
	for _, raw := range members {
		var j queue.Job
		if err := json.Unmarshal([]byte(raw), &j); err != nil {
			continue
		}
		if j.ID != jobID {
			continue
		}
		if onFound != nil {
			if err := onFound(j); err != nil {
				return err
			}
		}
		if err := q.client.ZRem(ctx, q.reservedKey(), raw).Err(); err != nil {
			return fmt.Errorf("redis-queue: zrem reserved: %w", err)
		}
		return nil
	}
	return nil
}

// Len returns ready + delayed + reserved counts.
func (q *Queue) Len() int {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	r, _ := q.client.LLen(ctx, q.readyKey()).Result()
	d, _ := q.client.ZCard(ctx, q.delayedKey()).Result()
	v, _ := q.client.ZCard(ctx, q.reservedKey()).Result()
	return int(r + d + v)
}

// Purge removes every job (ready, delayed, reserved) for this queue.
// Useful in tests.
func (q *Queue) Purge(ctx context.Context) error {
	return q.client.Del(ctx, q.readyKey(), q.delayedKey(), q.reservedKey()).Err()
}
