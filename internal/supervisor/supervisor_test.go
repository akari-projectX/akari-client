package supervisor

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/akari-projectX/akari-client/internal/clock"
)

type fakeCore struct {
	mu        sync.Mutex
	startErrs []error // consumed per Start call; nil entry = success
	healthy   bool
	starts    int
	stops     int
}

func (f *fakeCore) Start() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.starts++
	if len(f.startErrs) > 0 {
		err := f.startErrs[0]
		f.startErrs = f.startErrs[1:]
		if err != nil {
			return err
		}
	}
	f.healthy = true
	return nil
}

func (f *fakeCore) Stop() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stops++
	f.healthy = false
	return nil
}

func (f *fakeCore) Healthy(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.healthy {
		return errors.New("dead")
	}
	return nil
}

func (f *fakeCore) crash() {
	f.mu.Lock()
	f.healthy = false
	f.mu.Unlock()
}

func (f *fakeCore) counts() (int, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.starts, f.stops
}

func setup(t *testing.T, fc *fakeCore) (*Supervisor, *clock.Fake, context.CancelFunc, chan State) {
	t.Helper()
	clk := clock.NewFake(time.Unix(1_700_000_000, 0))
	events := make(chan State, 100)
	s := New(fc, clk, Config{HealthEvery: 10 * time.Second, HealthFailures: 2, BackoffMin: time.Second, BackoffMax: 8 * time.Second, StableAfter: time.Minute},
		func(st State, _ error) { events <- st })
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	return s, clk, cancel, events
}

func waitTimer(t *testing.T, clk *clock.Fake, want time.Duration) {
	t.Helper()
	if !clk.BlockUntil(1, 2*time.Second) {
		t.Fatal("no timer armed")
	}
	got, _ := clk.NextDeadline()
	if got != want {
		t.Fatalf("next timer = %v, want %v", got, want)
	}
}

func TestBackoffSequence(t *testing.T) {
	c := Config{BackoffMin: time.Second, BackoffMax: 60 * time.Second}
	want := []time.Duration{1, 2, 4, 8, 16, 32, 60, 60}
	for i, w := range want {
		if got := c.Backoff(i + 1); got != w*time.Second {
			t.Errorf("Backoff(%d) = %v, want %v", i+1, got, w*time.Second)
		}
	}
}

func TestStartFailureBacksOffThenRecovers(t *testing.T) {
	boom := errors.New("port in use")
	fc := &fakeCore{startErrs: []error{boom, boom, boom}}
	s, clk, _, _ := setup(t, fc)

	if err := s.Up(context.Background()); !errors.Is(err, boom) {
		t.Fatalf("Up = %v", err)
	}
	if st, err := s.State(); st != Backoff || err == nil {
		t.Fatalf("state = %v %v", st, err)
	}
	waitTimer(t, clk, time.Second)
	clk.Advance(time.Second) // retry #2 fails
	waitTimer(t, clk, 2*time.Second)
	clk.Advance(2 * time.Second) // retry #3 fails
	waitTimer(t, clk, 4*time.Second)
	clk.Advance(4 * time.Second) // retry #4 succeeds
	waitTimer(t, clk, 10*time.Second)
	if st, _ := s.State(); st != Running {
		t.Fatalf("state = %v", st)
	}
	if starts, _ := fc.counts(); starts != 4 {
		t.Fatalf("starts = %d", starts)
	}
}

func TestCrashIsRestarted(t *testing.T) {
	fc := &fakeCore{}
	s, clk, _, _ := setup(t, fc)
	if err := s.Up(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitTimer(t, clk, 10*time.Second)
	fc.crash()
	clk.Advance(10 * time.Second) // health fail 1
	waitTimer(t, clk, 10*time.Second)
	if st, _ := s.State(); st != Running {
		t.Fatalf("restarted after a single failed check: %v", st)
	}
	clk.Advance(10 * time.Second) // health fail 2 -> stop, backoff 1s
	waitTimer(t, clk, time.Second)
	if st, err := s.State(); st != Backoff || err == nil {
		t.Fatalf("state = %v %v", st, err)
	}
	clk.Advance(time.Second) // restart
	waitTimer(t, clk, 10*time.Second)
	starts, stops := fc.counts()
	if st, _ := s.State(); st != Running || starts != 2 || stops != 1 {
		t.Fatalf("state=%v starts=%d stops=%d", st, starts, stops)
	}

	// A second crash soon after backs off longer (2s) ...
	fc.crash()
	clk.Advance(10 * time.Second)
	waitTimer(t, clk, 10*time.Second)
	clk.Advance(10 * time.Second)
	waitTimer(t, clk, 2*time.Second)
	clk.Advance(2 * time.Second)
	waitTimer(t, clk, 10*time.Second)

	// ... and after StableAfter of healthy running the backoff resets.
	for i := 0; i < 7; i++ {
		clk.Advance(10 * time.Second)
		waitTimer(t, clk, 10*time.Second)
	}
	fc.crash()
	clk.Advance(10 * time.Second)
	waitTimer(t, clk, 10*time.Second)
	clk.Advance(10 * time.Second)
	waitTimer(t, clk, time.Second)
}

func TestDownCancelsRetries(t *testing.T) {
	fc := &fakeCore{startErrs: []error{errors.New("x")}}
	s, clk, _, _ := setup(t, fc)
	_ = s.Up(context.Background())
	waitTimer(t, clk, time.Second)
	if err := s.Down(context.Background()); err != nil {
		t.Fatal(err)
	}
	if st, _ := s.State(); st != Stopped {
		t.Fatalf("state = %v", st)
	}
	time.Sleep(10 * time.Millisecond)
	if clk.Pending() != 0 {
		t.Fatal("timer armed while down")
	}
	clk.Advance(time.Hour)
	if starts, _ := fc.counts(); starts != 1 {
		t.Fatalf("starts = %d", starts)
	}
}

func TestReloadAndShutdown(t *testing.T) {
	fc := &fakeCore{}
	s, _, cancel, _ := setup(t, fc)
	// Reload while down is a no-op.
	if err := s.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	if starts, _ := fc.counts(); starts != 0 {
		t.Fatal("reload started a stopped core")
	}
	_ = s.Up(context.Background())
	_ = s.Up(context.Background()) // idempotent
	if err := s.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	if starts, _ := fc.counts(); starts != 2 {
		t.Fatalf("starts = %d", starts)
	}
	cancel()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, stops := fc.counts(); stops == 1 {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("core not stopped on shutdown")
}

type exitingCore struct {
	fakeCore
	mu   sync.Mutex
	done chan struct{}
}

func (e *exitingCore) Start() error {
	e.mu.Lock()
	e.done = make(chan struct{})
	e.mu.Unlock()
	return e.fakeCore.Start()
}

func (e *exitingCore) Exited() <-chan struct{} {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.done
}

func (e *exitingCore) die() {
	e.mu.Lock()
	close(e.done)
	e.mu.Unlock()
	e.crash()
}

func TestExitRestartsWithoutHealthChecks(t *testing.T) {
	ec := &exitingCore{}
	clk := clock.NewFake(time.Unix(1_700_000_000, 0))
	s := New(ec, clk, Config{HealthEvery: 10 * time.Second}, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()
	defer func() { cancel(); <-done }()
	if err := s.Up(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitTimer(t, clk, 10*time.Second)
	ec.die() // no clock advance: the exit alone triggers the restart path
	deadline := time.Now().Add(2 * time.Second)
	for {
		if st, err := s.State(); st == Backoff && err != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("exit not noticed")
		}
		time.Sleep(time.Millisecond)
	}
	waitTimer(t, clk, time.Second)
	clk.Advance(time.Second)
	waitTimer(t, clk, 10*time.Second)
	if st, _ := s.State(); st != Running {
		t.Fatalf("state = %v", st)
	}
}
