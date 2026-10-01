package watchdog

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/akari-projectX/akari-client/internal/clock"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

type script struct {
	mu    sync.Mutex
	codes []int
	n     int
}

func (s *script) spawn(context.Context) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.codes[s.n]
	s.n++
	return c, nil
}

func runAsync(clk clock.Clock, p Policy, sp Spawner) (chan int, chan error) {
	codes, errs := make(chan int, 1), make(chan error, 1)
	go func() {
		c, err := Run(context.Background(), quiet, clk, p, sp)
		codes <- c
		errs <- err
	}()
	return codes, errs
}

func TestRestartsCrashesWithBackoffUntilCleanExit(t *testing.T) {
	clk := clock.NewFake(time.Unix(0, 0))
	s := &script{codes: []int{2, 2, 2, ExitOK}}
	codes, errs := runAsync(clk, Policy{}, s.spawn)
	for _, d := range []time.Duration{time.Second, 2 * time.Second, 4 * time.Second} {
		if !clk.BlockUntil(1, 2*time.Second) {
			t.Fatal("no backoff timer")
		}
		if got, _ := clk.NextDeadline(); got != d {
			t.Fatalf("backoff = %v, want %v", got, d)
		}
		clk.Advance(d)
	}
	if c := <-codes; c != ExitOK || <-errs != nil || s.n != 4 {
		t.Fatalf("code=%d n=%d", c, s.n)
	}
}

func TestNoRestartCodes(t *testing.T) {
	for _, code := range []int{ExitOK, ExitAlreadyRunning, ExitFatal} {
		s := &script{codes: []int{code}}
		c, err := Run(context.Background(), quiet, clock.NewFake(time.Unix(0, 0)), Policy{}, s.spawn)
		if c != code || err != nil || s.n != 1 {
			t.Fatalf("code %d: got %d %v n=%d", code, c, err, s.n)
		}
	}
	c, err := Run(context.Background(), quiet, clock.Real{}, Policy{}, func(context.Context) (int, error) { return 0, errors.New("exec failed") })
	if c != ExitFatal || err == nil {
		t.Fatal("spawn error not fatal")
	}
}

func TestGivesUpAfterCrashBudget(t *testing.T) {
	clk := clock.NewFake(time.Unix(0, 0))
	s := &script{codes: []int{2, 2, 2, 2, 2, 2, 2}}
	codes, errs := runAsync(clk, Policy{MaxCrashes: 3, BackoffMin: time.Second, BackoffMax: time.Second}, s.spawn)
	for i := 0; i < 2; i++ {
		clk.BlockUntil(1, 2*time.Second)
		clk.Advance(time.Second)
	}
	if c := <-codes; c != 2 || !errors.Is(<-errs, ErrGaveUp) || s.n != 3 {
		t.Fatalf("code=%d n=%d", c, s.n)
	}
}

func TestExitCode(t *testing.T) {
	if exitCode(nil) != 0 || exitCode(errors.New("x")) != 1 {
		t.Fatal("exitCode")
	}
}
