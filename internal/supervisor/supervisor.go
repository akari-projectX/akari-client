// Package supervisor keeps the proxy core in the user's desired state:
// it starts it, health-checks it, and restarts it with exponential backoff
// when it fails to start or stops answering.
//
// mihomo runs in-process, so a Go panic inside one of its goroutines would
// terminate the whole process; that case is covered one level up by the
// process watchdog (internal/watchdog). This package handles everything
// that does not kill the process: start errors (port in use, bad profile),
// a listener that died, and explicit reloads.
package supervisor

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/akari-projectX/akari-client/internal/clock"
)

// State is the externally visible core state.
type State int

const (
	Stopped State = iota
	Running
	// Backoff: the core should run but failed; a retry is scheduled.
	Backoff
)

func (s State) String() string {
	switch s {
	case Running:
		return "running"
	case Backoff:
		return "retrying"
	default:
		return "stopped"
	}
}

// Core is what the supervisor drives (internal/core.Engine in production,
// a fake in tests).
type Core interface {
	Start() error
	Stop() error
	Healthy(ctx context.Context) error
}

// Config tunes the loop. Zero values select the defaults.
type Config struct {
	HealthEvery time.Duration // default 10s
	// HealthFailures consecutive failed checks trigger a restart (default 2).
	HealthFailures int
	BackoffMin     time.Duration // default 1s
	BackoffMax     time.Duration // default 60s
	// StableAfter of healthy running resets the backoff (default 2m).
	StableAfter time.Duration
}

func (c *Config) defaults() {
	if c.HealthEvery <= 0 {
		c.HealthEvery = 10 * time.Second
	}
	if c.HealthFailures <= 0 {
		c.HealthFailures = 2
	}
	if c.BackoffMin <= 0 {
		c.BackoffMin = time.Second
	}
	if c.BackoffMax <= 0 {
		c.BackoffMax = time.Minute
	}
	if c.StableAfter <= 0 {
		c.StableAfter = 2 * time.Minute
	}
}

// Backoff returns the delay before retry number n (n >= 1).
func (c Config) Backoff(n int) time.Duration {
	d := c.BackoffMin
	for i := 1; i < n && d < c.BackoffMax; i++ {
		d *= 2
	}
	if d > c.BackoffMax {
		d = c.BackoffMax
	}
	return d
}

type cmd int

const (
	cmdUp cmd = iota
	cmdDown
	cmdReload
)

// Supervisor owns the core's lifecycle. All Core calls happen on the Run
// goroutine.
type Supervisor struct {
	core  Core
	clk   clock.Clock
	cfg   Config
	cmds  chan request
	onEvt func(State, error)

	mu      sync.Mutex
	state   State
	lastErr error
}

type request struct {
	c    cmd
	done chan error
}

// New creates a supervisor. onEvent (may be nil) is called on the Run
// goroutine after every state change or failure.
func New(core Core, clk clock.Clock, cfg Config, onEvent func(State, error)) *Supervisor {
	cfg.defaults()
	if onEvent == nil {
		onEvent = func(State, error) {}
	}
	return &Supervisor{core: core, clk: clk, cfg: cfg, cmds: make(chan request), onEvt: onEvent}
}

// State returns the current state and the last error (nil when healthy).
func (s *Supervisor) State() (State, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state, s.lastErr
}

// ErrNotRunning is returned by commands after Run has exited.
var ErrNotRunning = errors.New("supervisor not running")

func (s *Supervisor) send(ctx context.Context, c cmd) error {
	r := request{c: c, done: make(chan error, 1)}
	select {
	case s.cmds <- r:
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case err := <-r.done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Up makes the core run; returns the result of the first start attempt
// (on failure the supervisor keeps retrying in the background).
func (s *Supervisor) Up(ctx context.Context) error { return s.send(ctx, cmdUp) }

// Down stops the core and cancels retries.
func (s *Supervisor) Down(ctx context.Context) error { return s.send(ctx, cmdDown) }

// Reload re-applies the core (new profile/port) if it should be running,
// resetting the backoff.
func (s *Supervisor) Reload(ctx context.Context) error { return s.send(ctx, cmdReload) }

func (s *Supervisor) set(st State, err error) {
	s.mu.Lock()
	changed := s.state != st || (err != nil) != (s.lastErr != nil) || (err != nil && s.lastErr != nil && err.Error() != s.lastErr.Error())
	s.state, s.lastErr = st, err
	s.mu.Unlock()
	if changed || err != nil {
		s.onEvt(st, err)
	}
}

// Run executes the loop until ctx is done; the core is stopped on exit.
func (s *Supervisor) Run(ctx context.Context) {
	var (
		want       bool
		running    bool
		failures   int // consecutive start failures / crashes
		healthBad  int
		healthyFor time.Time // when the current run started
	)
	start := func() error {
		err := s.core.Start()
		if err != nil {
			running = false
			failures++
			s.set(Backoff, err)
			return err
		}
		running = true
		healthBad = 0
		healthyFor = s.clk.Now()
		s.set(Running, nil)
		return nil
	}
	defer func() {
		if running {
			_ = s.core.Stop()
		}
		s.set(Stopped, nil)
	}()

	for {
		var wait time.Duration
		switch {
		case !want:
			wait = -1
		case running:
			wait = s.cfg.HealthEvery
		default:
			wait = s.cfg.Backoff(max(failures, 1))
		}
		var timerC <-chan time.Time
		var t clock.Timer
		if wait >= 0 {
			t = s.clk.NewTimer(wait)
			timerC = t.C()
		}
		select {
		case <-ctx.Done():
			if t != nil {
				t.Stop()
			}
			return
		case r := <-s.cmds:
			if t != nil {
				t.Stop()
			}
			switch r.c {
			case cmdUp:
				want = true
				if running {
					r.done <- nil
					continue
				}
				failures = 0
				r.done <- start()
			case cmdDown:
				want = false
				failures = 0
				var err error
				if running {
					err = s.core.Stop()
					running = false
				}
				s.set(Stopped, nil)
				r.done <- err
			case cmdReload:
				if !want {
					r.done <- nil
					continue
				}
				failures = 0
				r.done <- start()
			}
		case <-timerC:
			if !want {
				continue
			}
			if !running {
				_ = start()
				continue
			}
			hctx, cancel := context.WithTimeout(ctx, 5*time.Second)
			err := s.core.Healthy(hctx)
			cancel()
			if err == nil {
				healthBad = 0
				if failures > 0 && s.clk.Now().Sub(healthyFor) >= s.cfg.StableAfter {
					failures = 0
				}
				continue
			}
			healthBad++
			if healthBad < s.cfg.HealthFailures {
				continue
			}
			// Core is unhealthy: tear down and restart after backoff.
			_ = s.core.Stop()
			running = false
			failures++
			s.set(Backoff, err)
		}
	}
}
