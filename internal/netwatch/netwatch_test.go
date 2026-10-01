package netwatch

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/akari-projectX/akari-client/internal/clock"
)

func TestSleptBetween(t *testing.T) {
	base := time.Now() // has a monotonic reading
	if SleptBetween(base, base.Add(5*time.Second), 5*time.Second, 30*time.Second) {
		t.Fatal("normal tick flagged")
	}
	if !SleptBetween(base, base.Add(10*time.Minute), 5*time.Second, 30*time.Second) {
		t.Fatal("late tick not flagged")
	}
	// Wall clock jumped 2h while monotonic advanced 5s (suspend on Linux).
	wallOnly := base.Round(0).Add(2 * time.Hour)
	if !SleptBetween(base.Round(0), wallOnly, 5*time.Second, 30*time.Second) {
		t.Fatal("wall jump not flagged")
	}
}

func TestWatcherEvents(t *testing.T) {
	clk := clock.NewFake(time.Unix(1_700_000_000, 0))
	var mu sync.Mutex
	fp := "wlan0=192.168.1.5"
	var got []Reason
	w := &Watcher{
		Clock:       clk,
		Interval:    5 * time.Second,
		Fingerprint: func() string { mu.Lock(); defer mu.Unlock(); return fp },
		OnEvent:     func(r Reason) { mu.Lock(); got = append(got, r); mu.Unlock() },
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { w.Run(ctx); close(done) }()
	defer func() { cancel(); <-done }()

	step := func(d time.Duration) {
		t.Helper()
		if !clk.BlockUntil(1, 2*time.Second) {
			t.Fatal("no timer")
		}
		clk.Advance(d)
	}
	step(5 * time.Second) // no change
	mu.Lock()
	fp = "wlan0=10.0.0.7"
	mu.Unlock()
	step(5 * time.Second) // change
	step(5 * time.Second) // stable
	step(10 * time.Minute)
	clk.BlockUntil(1, 2*time.Second) // event delivered before next timer
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 2 || got[0] != NetworkChanged || got[1] != Resumed {
		t.Fatalf("events = %v", got)
	}
}

func TestAddrFingerprintStable(t *testing.T) {
	if AddrFingerprint() != AddrFingerprint() {
		t.Fatal("fingerprint not deterministic")
	}
}
