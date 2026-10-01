// Package watchdog restarts the client process if it dies abnormally.
//
// mihomo runs in-process; an unrecovered panic in one of its goroutines
// kills the whole client, which the in-process supervisor cannot catch.
// The default launch therefore runs a small parent that re-executes the
// same binary as the "child" (env ChildEnv=1) and restarts it with
// backoff on abnormal exit.
package watchdog

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"time"

	"github.com/akari-projectX/akari-client/internal/clock"
)

// ChildEnv marks the supervised child process.
const ChildEnv = "AKARI_CLIENT_CHILD"

// Exit codes with special meaning to the watchdog.
const (
	ExitOK             = 0 // user quit: do not restart
	ExitAlreadyRunning = 3 // another instance holds the lock: do not restart
	ExitFatal          = 4 // configuration/environment error: do not restart
)

// Policy bounds restarts.
type Policy struct {
	BackoffMin time.Duration // default 1s
	BackoffMax time.Duration // default 30s
	// MaxCrashes within Window make the watchdog give up (default 5 / 10m).
	MaxCrashes int
	Window     time.Duration
}

func (p *Policy) defaults() {
	if p.BackoffMin <= 0 {
		p.BackoffMin = time.Second
	}
	if p.BackoffMax <= 0 {
		p.BackoffMax = 30 * time.Second
	}
	if p.MaxCrashes <= 0 {
		p.MaxCrashes = 5
	}
	if p.Window <= 0 {
		p.Window = 10 * time.Minute
	}
}

// Spawner starts one child and waits for its exit code.
type Spawner func(ctx context.Context) (exitCode int, err error)

// ErrGaveUp is returned when the child keeps crashing.
var ErrGaveUp = errors.New("client crashed repeatedly; giving up")

// Run supervises children until one exits with a non-restart code, ctx is
// cancelled, or the crash budget is exhausted. It returns the exit code
// the parent should use.
func Run(ctx context.Context, log *slog.Logger, clk clock.Clock, p Policy, spawn Spawner) (int, error) {
	p.defaults()
	var crashes []time.Time
	backoff := p.BackoffMin
	for {
		started := clk.Now()
		code, err := spawn(ctx)
		if ctx.Err() != nil {
			return ExitOK, nil
		}
		switch {
		case err != nil:
			return ExitFatal, err // could not start at all
		case code == ExitOK || code == ExitAlreadyRunning || code == ExitFatal:
			return code, nil
		}
		now := clk.Now()
		if now.Sub(started) > p.Window {
			backoff = p.BackoffMin // it ran fine for a long time
		}
		crashes = append(crashes, now)
		for len(crashes) > 0 && now.Sub(crashes[0]) > p.Window {
			crashes = crashes[1:]
		}
		log.Error("client exited abnormally", "code", code, "recent_crashes", len(crashes))
		if len(crashes) >= p.MaxCrashes {
			return code, ErrGaveUp
		}
		t := clk.NewTimer(backoff)
		select {
		case <-ctx.Done():
			t.Stop()
			return ExitOK, nil
		case <-t.C():
		}
		backoff *= 2
		if backoff > p.BackoffMax {
			backoff = p.BackoffMax
		}
	}
}

// ExecSpawner re-executes the current binary with args as a child.
func ExecSpawner(args []string) (Spawner, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	return func(ctx context.Context) (int, error) {
		cmd := exec.Command(exe, args...)
		cmd.Env = append(os.Environ(), ChildEnv+"=1")
		// GUI-subsystem binaries on Windows have no std handles (nil *os.File).
		if os.Stdout != nil {
			cmd.Stdout = os.Stdout
		}
		if os.Stderr != nil {
			cmd.Stderr = os.Stderr
		}
		if err := cmd.Start(); err != nil {
			return 0, err
		}
		waited := make(chan error, 1)
		go func() { waited <- cmd.Wait() }()
		select {
		case err := <-waited:
			return exitCode(err), nil
		case <-ctx.Done():
			// Parent asked to stop: let the child shut down cleanly
			// (it reverts the system proxy), then kill it.
			_ = interrupt(cmd.Process)
			select {
			case err := <-waited:
				return exitCode(err), nil
			case <-time.After(10 * time.Second):
				_ = cmd.Process.Kill()
				return exitCode(<-waited), nil
			}
		}
	}, nil
}

func exitCode(err error) int {
	if err == nil {
		return 0
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		if c := ee.ExitCode(); c >= 0 {
			return c
		}
		return 128 // killed by a signal
	}
	return 1
}
