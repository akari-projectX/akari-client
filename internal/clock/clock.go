// Package clock abstracts time so schedulers and backoff loops can be
// tested deterministically.
package clock

import (
	"sort"
	"sync"
	"time"
)

// Clock is the subset of the time package the client uses.
type Clock interface {
	Now() time.Time
	NewTimer(d time.Duration) Timer
}

// Timer mirrors *time.Timer.
type Timer interface {
	C() <-chan time.Time
	Stop() bool
}

// Real is the wall clock.
type Real struct{}

func (Real) Now() time.Time { return time.Now() }

func (Real) NewTimer(d time.Duration) Timer { return realTimer{time.NewTimer(d)} }

type realTimer struct{ t *time.Timer }

func (r realTimer) C() <-chan time.Time { return r.t.C }
func (r realTimer) Stop() bool          { return r.t.Stop() }

// Fake is a manually advanced clock for tests.
type Fake struct {
	mu     sync.Mutex
	now    time.Time
	timers []*fakeTimer
}

// NewFake returns a fake clock starting at t.
func NewFake(t time.Time) *Fake { return &Fake{now: t} }

func (f *Fake) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

func (f *Fake) NewTimer(d time.Duration) Timer {
	f.mu.Lock()
	defer f.mu.Unlock()
	t := &fakeTimer{f: f, when: f.now.Add(d), c: make(chan time.Time, 1)}
	if d <= 0 {
		t.fired = true
		t.c <- f.now
		return t
	}
	f.timers = append(f.timers, t)
	return t
}

// Advance moves the clock forward, firing due timers in deadline order.
func (f *Fake) Advance(d time.Duration) {
	f.mu.Lock()
	f.now = f.now.Add(d)
	now := f.now
	sort.SliceStable(f.timers, func(i, j int) bool { return f.timers[i].when.Before(f.timers[j].when) })
	var keep []*fakeTimer
	var fire []*fakeTimer
	for _, t := range f.timers {
		if !t.when.After(now) {
			fire = append(fire, t)
		} else {
			keep = append(keep, t)
		}
	}
	f.timers = keep
	for _, t := range fire {
		t.fired = true
	}
	f.mu.Unlock()
	for _, t := range fire {
		select {
		case t.c <- now:
		default:
		}
	}
}

// Pending returns the number of armed (not fired, not stopped) timers.
func (f *Fake) Pending() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.timers)
}

// NextDeadline returns the earliest armed timer deadline relative to now.
func (f *Fake) NextDeadline() (time.Duration, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.timers) == 0 {
		return 0, false
	}
	min := f.timers[0].when
	for _, t := range f.timers[1:] {
		if t.when.Before(min) {
			min = t.when
		}
	}
	return min.Sub(f.now), true
}

type fakeTimer struct {
	f     *Fake
	when  time.Time
	c     chan time.Time
	fired bool
}

func (t *fakeTimer) C() <-chan time.Time { return t.c }

func (t *fakeTimer) Stop() bool {
	t.f.mu.Lock()
	defer t.f.mu.Unlock()
	if t.fired {
		return false
	}
	for i, x := range t.f.timers {
		if x == t {
			t.f.timers = append(t.f.timers[:i], t.f.timers[i+1:]...)
			return true
		}
	}
	return false
}

// BlockUntil waits (real time, up to timeout) until at least n timers are
// armed. Tests use it to sync with a goroutine before calling Advance.
func (f *Fake) BlockUntil(n int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if f.Pending() >= n {
			return true
		}
		time.Sleep(time.Millisecond)
	}
	return false
}
