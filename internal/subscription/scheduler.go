package subscription

import (
	"context"
	"time"

	"github.com/akari-projectX/akari-client/internal/clock"
)

// Retry backoff after a failed refresh: RetryMin doubling up to RetryMax,
// never later than the regular interval.
const (
	RetryMin = time.Minute
	RetryMax = 30 * time.Minute
)

// Scheduler runs Do periodically: Interval() after Last(), immediately on
// Trigger, and with exponential backoff after failures.
type Scheduler struct {
	Clock    clock.Clock
	Interval func() time.Duration
	// Last returns the time of the last successful fetch (zero = never).
	Last func() time.Time
	Do   func(ctx context.Context) error

	trigger chan struct{}
}

// NewScheduler wires a scheduler.
func NewScheduler(c clock.Clock, interval func() time.Duration, last func() time.Time, do func(context.Context) error) *Scheduler {
	return &Scheduler{Clock: c, Interval: interval, Last: last, Do: do, trigger: make(chan struct{}, 1)}
}

// Trigger requests an immediate refresh (coalesced if one is pending).
func (s *Scheduler) Trigger() {
	select {
	case s.trigger <- struct{}{}:
	default:
	}
}

// NextDelay computes the wait before the next attempt.
func NextDelay(now, last time.Time, interval time.Duration, failures int) time.Duration {
	if failures > 0 {
		d := RetryMin
		for i := 1; i < failures && d < RetryMax; i++ {
			d *= 2
		}
		if d > RetryMax {
			d = RetryMax
		}
		if d > interval {
			d = interval
		}
		return d
	}
	if last.IsZero() {
		return 0
	}
	d := last.Add(interval).Sub(now)
	if d < 0 {
		return 0
	}
	if d > interval { // clock moved backwards
		return interval
	}
	return d
}

// Run blocks until ctx is done.
func (s *Scheduler) Run(ctx context.Context) {
	failures := 0
	for {
		d := NextDelay(s.Clock.Now(), s.Last(), s.Interval(), failures)
		t := s.Clock.NewTimer(d)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C():
		case <-s.trigger:
			t.Stop()
		}
		if err := s.Do(ctx); err != nil {
			if ctx.Err() != nil {
				return
			}
			failures++
		} else {
			failures = 0
		}
	}
}
