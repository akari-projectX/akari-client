// Package netwatch detects events after which proxied connections are
// probably dead: a change of the machine's network addresses (Wi-Fi
// switch, VPN up/down, DHCP renew with a new address) and resume from
// sleep. It polls; no OS-specific notification APIs are needed.
package netwatch

import (
	"context"
	"net"
	"sort"
	"strings"
	"time"

	"github.com/akari-projectX/akari-client/internal/clock"
)

// Reason describes why an event fired.
type Reason string

const (
	NetworkChanged Reason = "network changed"
	Resumed        Reason = "resumed from sleep"
)

// Watcher polls every Interval.
type Watcher struct {
	Clock    clock.Clock
	Interval time.Duration // default 5s
	// SleepThreshold: a tick arriving this much later than scheduled (by
	// the monotonic or the wall clock) is treated as a resume (default 30s).
	SleepThreshold time.Duration
	// Fingerprint summarizes the network state (default: AddrFingerprint).
	Fingerprint func() string
	OnEvent     func(Reason)
}

// AddrFingerprint lists the addresses of all up, non-loopback interfaces.
func AddrFingerprint() string {
	ifs, err := net.Interfaces()
	if err != nil {
		return ""
	}
	var parts []string
	for _, ifc := range ifs {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := ifc.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipn, ok := a.(*net.IPNet)
			if !ok || ipn.IP.IsLinkLocalUnicast() {
				continue
			}
			parts = append(parts, ifc.Name+"="+ipn.IP.String())
		}
	}
	sort.Strings(parts)
	return strings.Join(parts, ",")
}

// SleptBetween decides whether the machine slept between two ticks taken
// expected apart. now.Sub(prev) uses the monotonic clock when both values
// carry one (which stops during suspend on Linux/macOS); Round(0) strips
// it to compare wall time (which keeps running).
func SleptBetween(prev, now time.Time, expected, threshold time.Duration) bool {
	mono := now.Sub(prev)
	wall := now.Round(0).Sub(prev.Round(0))
	return mono > expected+threshold || wall-mono > threshold
}

// Run polls until ctx is done.
func (w *Watcher) Run(ctx context.Context) {
	if w.Interval <= 0 {
		w.Interval = 5 * time.Second
	}
	if w.SleepThreshold <= 0 {
		w.SleepThreshold = 30 * time.Second
	}
	if w.Fingerprint == nil {
		w.Fingerprint = AddrFingerprint
	}
	last := w.Fingerprint()
	prev := w.Clock.Now()
	for {
		t := w.Clock.NewTimer(w.Interval)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C():
		}
		now := w.Clock.Now()
		slept := SleptBetween(prev, now, w.Interval, w.SleepThreshold)
		prev = now
		fp := w.Fingerprint()
		changed := fp != last
		last = fp
		switch {
		case slept:
			w.OnEvent(Resumed)
		case changed:
			w.OnEvent(NetworkChanged)
		}
	}
}
