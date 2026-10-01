// Package app wires subscription, core supervisor, system proxy and
// self-healing together and exposes the actions the UI needs.
package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/akari-projectX/akari-client/internal/clock"
	"github.com/akari-projectX/akari-client/internal/core"
	"github.com/akari-projectX/akari-client/internal/netwatch"
	"github.com/akari-projectX/akari-client/internal/settings"
	"github.com/akari-projectX/akari-client/internal/store"
	"github.com/akari-projectX/akari-client/internal/subscription"
	"github.com/akari-projectX/akari-client/internal/supervisor"
	"github.com/akari-projectX/akari-client/internal/sysproxy"
)

// ProfileFile holds the last good subscription (contains node
// credentials; 0600).
const ProfileFile = "profile.yaml"

// Engine is the subset of core.Engine the app uses (faked in tests).
type Engine interface {
	Start(sub []byte, opts core.Options) error
	Stop() error
	Healthy(ctx context.Context) error
	Nodes(testURL string) (string, []core.Node, error)
	Select(node string) error
	DelayTest(ctx context.Context, node, testURL string) (time.Duration, error)
	DelayTestAll(ctx context.Context, testURL string, timeout time.Duration) map[string]time.Duration
	ResetNetwork()
}

// Deps are the app's collaborators.
type Deps struct {
	Log      *slog.Logger
	Dir      string // settings/profile directory
	Settings *settings.Store
	Engine   Engine
	Validate func([]byte) error // core.Validate
	Fetcher  *subscription.Fetcher
	SysProxy sysproxy.Manager
	Clock    clock.Clock
	// Supervisor tuning (zero = defaults).
	Supervisor supervisor.Config
	// KernelLogLevel is mihomo's log level (default "warning": "info" logs
	// every destination host, which we do not want on disk by default).
	KernelLogLevel string
	// Netwatch enables network-change/resume self-healing (nil = off).
	Netwatch *netwatch.Watcher
}

// Status is a snapshot for the UI.
type Status struct {
	Configured  bool
	Core        supervisor.State
	CoreErr     string
	SystemProxy bool
	SysProxyErr string
	Port        int
	Selected    string
	Nodes       []core.Node
	UserInfo    *settings.UserInfo
	LastFetch   time.Time
	SubErr      string
	PanelHost   string
}

// App is the client's controller.
type App struct {
	d   Deps
	log *slog.Logger
	sup *supervisor.Supervisor
	sch *subscription.Scheduler

	mu          sync.Mutex
	profile     []byte
	subErr      string
	sysProxyErr string
	sysProxyOn  bool // we currently have the OS proxy pointed at us
	sysProxyEP  sysproxy.Endpoint
	spMu        sync.Mutex // serializes OS proxy changes

	changed chan struct{}
}

// New builds the app; call Run to start it.
func New(d Deps) (*App, error) {
	if d.Clock == nil {
		d.Clock = clock.Real{}
	}
	a := &App{d: d, log: d.Log, changed: make(chan struct{}, 1)}
	b, err := os.ReadFile(filepath.Join(d.Dir, ProfileFile))
	switch {
	case err == nil:
		if verr := d.Validate(b); verr != nil {
			a.log.Warn("stored profile is invalid; waiting for a refresh", "err", verr)
		} else {
			a.profile = b
		}
	case !errors.Is(err, os.ErrNotExist):
		return nil, fmt.Errorf("read profile: %w", err)
	}
	a.sup = supervisor.New(coreAdapter{a}, d.Clock, d.Supervisor, a.onCoreEvent)
	a.sch = subscription.NewScheduler(d.Clock,
		func() time.Duration { return d.Settings.Get().Refresh() },
		func() time.Time {
			s := d.Settings.Get()
			if s.SubscriptionURL == "" {
				return d.Clock.Now() // nothing to refresh; wait for Login
			}
			return s.LastFetch
		},
		a.refresh)
	if d.Fetcher.ViaCore == nil {
		d.Fetcher.ViaCore = a.viaCoreClient
	}
	return a, nil
}

// Changes signals (coalesced) whenever Status may have changed.
func (a *App) Changes() <-chan struct{} { return a.changed }

func (a *App) notify() {
	select {
	case a.changed <- struct{}{}:
	default:
	}
}

// Run starts background loops, restores the previous session and blocks
// until ctx is done; on return the system proxy is reverted and the core
// is stopped.
func (a *App) Run(ctx context.Context) {
	var wg sync.WaitGroup
	supCtx, supCancel := context.WithCancel(context.Background())
	wg.Add(1)
	go func() { defer wg.Done(); a.sup.Run(supCtx) }()
	wg.Add(1)
	go func() { defer wg.Done(); a.sch.Run(ctx) }()
	if a.d.Netwatch != nil {
		a.d.Netwatch.OnEvent = func(r netwatch.Reason) { a.heal(ctx, r) }
		wg.Add(1)
		go func() { defer wg.Done(); a.d.Netwatch.Run(ctx) }()
	}

	s := a.d.Settings.Get()
	ep := a.endpoint(s)
	if !s.SystemProxy {
		// A previous crash may have left the OS proxy pointing at us.
		if err := a.d.SysProxy.Disable(ctx, ep); err != nil {
			a.log.Debug("system proxy cleanup", "err", err)
		}
	}
	if s.Connect && a.hasProfile() {
		if err := a.Connect(ctx); err != nil {
			a.log.Warn("auto-connect failed (will retry)", "err", err)
		}
	}
	a.notify()

	<-ctx.Done()
	sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	a.revertSystemProxy(sctx)
	supCancel()
	wg.Wait()
}

func (a *App) hasProfile() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.profile) > 0
}

func (a *App) endpoint(s settings.Settings) sysproxy.Endpoint {
	return sysproxy.Endpoint{Host: "127.0.0.1", Port: s.Port()}
}

// coreAdapter lets the supervisor drive the engine with the current
// profile and settings.
type coreAdapter struct{ a *App }

func (c coreAdapter) Start() error {
	a := c.a
	a.mu.Lock()
	prof := a.profile
	a.mu.Unlock()
	if len(prof) == 0 {
		return errors.New("no subscription profile yet")
	}
	s := a.d.Settings.Get()
	lvl := a.d.KernelLogLevel
	if lvl == "" {
		lvl = "warning"
	}
	if err := a.d.Engine.Start(prof, core.Options{MixedPort: s.Port(), LogLevel: lvl}); err != nil {
		return err
	}
	if s.SelectedNode != "" {
		if err := a.d.Engine.Select(s.SelectedNode); err != nil {
			a.log.Info("saved node no longer offered; using default", "node", s.SelectedNode)
		}
	}
	return nil
}

func (c coreAdapter) Stop() error { return c.a.d.Engine.Stop() }

// Exited forwards the engine's exit notification when it has one.
func (c coreAdapter) Exited() <-chan struct{} {
	if ex, ok := c.a.d.Engine.(supervisor.Exiter); ok {
		return ex.Exited()
	}
	return nil
}
func (c coreAdapter) Healthy(ctx context.Context) error { return c.a.d.Engine.Healthy(ctx) }

func (a *App) onCoreEvent(st supervisor.State, err error) {
	if err != nil {
		a.log.Warn("core", "state", st.String(), "err", err)
	} else {
		a.log.Info("core", "state", st.String())
	}
	if st == supervisor.Running {
		// (Re)apply the system proxy after a (re)start.
		s := a.d.Settings.Get()
		if s.SystemProxy {
			go a.applySystemProxy(context.Background(), s)
		}
	}
	a.notify()
}

// Login enrolls the subscription and connects (reloading the kernel when
// it already runs another profile).
func (a *App) Login(ctx context.Context, input, token string) error {
	if err := a.Enroll(ctx, input, token); err != nil {
		return err
	}
	if st, _ := a.sup.State(); st == supervisor.Running {
		return a.sup.Reload(ctx)
	}
	return a.Connect(ctx)
}

// Enroll validates the input, fetches and validates the profile and
// persists it with the subscription URL (Connect=true, so the next launch
// connects). It does not start the core.
func (a *App) Enroll(ctx context.Context, input, token string) error {
	u, err := subscription.Normalize(input, token)
	if err != nil {
		return err
	}
	res, err := a.d.Fetcher.Fetch(ctx, u, "")
	if err != nil {
		return err
	}
	if err := a.d.Validate(res.Body); err != nil {
		return err
	}
	if err := store.WriteFile(filepath.Join(a.d.Dir, ProfileFile), res.Body); err != nil {
		return fmt.Errorf("save profile: %w", err)
	}
	now := a.d.Clock.Now()
	if _, err := a.d.Settings.Update(func(s *settings.Settings) {
		if s.SubscriptionURL != u {
			s.SelectedNode = ""
		}
		s.SubscriptionURL = u
		s.ETag = res.ETag
		s.LastFetch = now
		s.UserInfo = res.UserInfo
		s.PanelInterval = res.IntervalHours
		s.Connect = true
	}); err != nil {
		return err
	}
	a.mu.Lock()
	a.profile = res.Body
	a.subErr = ""
	a.mu.Unlock()
	a.log.Info("subscription enrolled", "panel", subscription.Redact(u))
	a.notify()
	return nil
}

// Logout disconnects and forgets the subscription and profile.
func (a *App) Logout(ctx context.Context) error {
	if err := a.Disconnect(ctx); err != nil {
		a.log.Warn("disconnect on logout", "err", err)
	}
	if _, err := a.d.Settings.Update(func(s *settings.Settings) {
		*s = settings.Settings{MixedPort: s.MixedPort, RefreshMinutes: s.RefreshMinutes, TestURL: s.TestURL, SystemProxy: s.SystemProxy}
	}); err != nil {
		return err
	}
	a.mu.Lock()
	a.profile = nil
	a.subErr = ""
	a.mu.Unlock()
	_ = os.Remove(filepath.Join(a.d.Dir, ProfileFile))
	a.notify()
	return nil
}

// RefreshNow triggers an immediate subscription refresh.
func (a *App) RefreshNow() { a.sch.Trigger() }

// refresh is the scheduler's job.
func (a *App) refresh(ctx context.Context) error {
	s := a.d.Settings.Get()
	if s.SubscriptionURL == "" {
		return nil
	}
	fctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	etag := s.ETag
	if !a.hasProfile() {
		etag = ""
	}
	res, err := a.d.Fetcher.Fetch(fctx, s.SubscriptionURL, etag)
	if err == nil && !res.NotModified {
		err = a.d.Validate(res.Body)
	}
	if err != nil {
		a.setSubErr(err)
		a.log.Warn("subscription refresh failed; keeping current profile", "panel", subscription.Redact(s.SubscriptionURL), "err", err)
		return err
	}
	now := a.d.Clock.Now()
	if !res.NotModified {
		if err := store.WriteFile(filepath.Join(a.d.Dir, ProfileFile), res.Body); err != nil {
			a.setSubErr(err)
			return err
		}
	}
	if _, err := a.d.Settings.Update(func(x *settings.Settings) {
		x.ETag = res.ETag
		x.LastFetch = now
		if res.UserInfo != nil {
			x.UserInfo = res.UserInfo
		}
		if res.IntervalHours > 0 {
			x.PanelInterval = res.IntervalHours
		}
	}); err != nil {
		a.setSubErr(err)
		return err
	}
	a.setSubErr(nil)
	if res.NotModified {
		a.log.Info("subscription unchanged")
		return nil
	}
	a.mu.Lock()
	prev := a.profile
	changed := string(prev) != string(res.Body)
	a.profile = res.Body
	a.mu.Unlock()
	a.log.Info("subscription updated", "changed", changed)
	if !changed {
		return nil
	}
	if err := a.sup.Reload(ctx); err != nil && len(prev) > 0 {
		// The kernel refused the new profile: go back to the previous one
		// and forget the ETag so the next refresh fetches in full.
		a.log.Warn("new profile failed to load; reverting to the previous one", "err", err)
		a.mu.Lock()
		a.profile = prev
		a.mu.Unlock()
		_ = store.WriteFile(filepath.Join(a.d.Dir, ProfileFile), prev)
		_, _ = a.d.Settings.Update(func(x *settings.Settings) { x.ETag = "" })
		if rerr := a.sup.Reload(ctx); rerr != nil {
			a.log.Warn("reload of previous profile failed (retrying)", "err", rerr)
		}
		a.setSubErr(fmt.Errorf("new profile rejected by the kernel: %w", err))
		return err
	}
	return nil
}

func (a *App) setSubErr(err error) {
	a.mu.Lock()
	if err == nil {
		a.subErr = ""
	} else {
		a.subErr = err.Error()
	}
	a.mu.Unlock()
	a.notify()
}

// Connect starts the core (and the system proxy if enabled).
func (a *App) Connect(ctx context.Context) error {
	if !a.hasProfile() {
		return errors.New("not logged in: enter your subscription URL first")
	}
	if _, err := a.d.Settings.Update(func(s *settings.Settings) { s.Connect = true }); err != nil {
		return err
	}
	// System proxy is applied from onCoreEvent(Running).
	return a.sup.Up(ctx)
}

// Disconnect reverts the system proxy, then stops the core.
func (a *App) Disconnect(ctx context.Context) error {
	if _, err := a.d.Settings.Update(func(s *settings.Settings) { s.Connect = false }); err != nil {
		return err
	}
	a.revertSystemProxy(ctx)
	return a.sup.Down(ctx)
}

// SetSystemProxy toggles (and persists) the system proxy preference.
func (a *App) SetSystemProxy(ctx context.Context, on bool) error {
	s, err := a.d.Settings.Update(func(s *settings.Settings) { s.SystemProxy = on })
	if err != nil {
		return err
	}
	if !on {
		a.revertSystemProxy(ctx)
		return nil
	}
	if st, _ := a.sup.State(); st == supervisor.Running {
		return a.applySystemProxy(ctx, s)
	}
	a.notify()
	return nil
}

func (a *App) applySystemProxy(ctx context.Context, _ settings.Settings) error {
	a.spMu.Lock()
	defer a.spMu.Unlock()
	// Re-read under spMu: a Disconnect or toggle may have raced us.
	s := a.d.Settings.Get()
	if !s.Connect || !s.SystemProxy {
		return nil
	}
	ep := a.endpoint(s)
	a.mu.Lock()
	if a.sysProxyOn && a.sysProxyEP == ep {
		a.mu.Unlock()
		return nil
	}
	a.mu.Unlock()
	err := a.d.SysProxy.Enable(ctx, ep)
	a.mu.Lock()
	if err != nil {
		a.sysProxyErr = err.Error()
	} else {
		a.sysProxyErr = ""
		a.sysProxyOn = true
		a.sysProxyEP = ep
	}
	a.mu.Unlock()
	if err != nil {
		a.log.Warn("enable system proxy", "err", err)
	} else {
		a.log.Info("system proxy enabled", "endpoint", ep.String())
	}
	a.notify()
	return err
}

func (a *App) revertSystemProxy(ctx context.Context) {
	a.spMu.Lock()
	defer a.spMu.Unlock()
	a.mu.Lock()
	on, ep := a.sysProxyOn, a.sysProxyEP
	a.mu.Unlock()
	if !on {
		return
	}
	if err := a.d.SysProxy.Disable(ctx, ep); err != nil {
		a.log.Warn("disable system proxy", "err", err)
		a.mu.Lock()
		a.sysProxyErr = err.Error()
		a.mu.Unlock()
	} else {
		a.log.Info("system proxy disabled")
	}
	a.mu.Lock()
	a.sysProxyOn = false
	a.mu.Unlock()
	a.notify()
}

// SetPort changes the mixed port, moving the system proxy along.
func (a *App) SetPort(ctx context.Context, port int) error {
	cur := a.d.Settings.Get()
	if cur.Port() == port {
		return nil
	}
	s, err := a.d.Settings.Update(func(s *settings.Settings) { s.MixedPort = port })
	if err != nil {
		return err
	}
	a.revertSystemProxy(ctx)
	err = a.sup.Reload(ctx)
	if err == nil && s.SystemProxy {
		if st, _ := a.sup.State(); st == supervisor.Running {
			_ = a.applySystemProxy(ctx, s)
		}
	}
	return err
}

// SetRefreshMinutes overrides the refresh interval (0 = panel default).
func (a *App) SetRefreshMinutes(m int) error {
	_, err := a.d.Settings.Update(func(s *settings.Settings) { s.RefreshMinutes = m })
	return err
}

// SelectNode switches the main group and persists the choice.
func (a *App) SelectNode(name string) error {
	if err := a.d.Engine.Select(name); err != nil {
		return err
	}
	_, err := a.d.Settings.Update(func(s *settings.Settings) { s.SelectedNode = name })
	a.log.Info("node selected", "node", name)
	a.notify()
	return err
}

// TestDelays measures every node of the main group.
func (a *App) TestDelays(ctx context.Context) map[string]time.Duration {
	res := a.d.Engine.DelayTestAll(ctx, a.d.Settings.Get().TestTarget(), 5*time.Second)
	a.notify()
	return res
}

// heal runs after a network change or resume: drop dead connections,
// probe the selected node and reload the core if the probe fails.
func (a *App) heal(ctx context.Context, r netwatch.Reason) {
	if st, _ := a.sup.State(); st != supervisor.Running {
		return
	}
	a.log.Info("self-heal", "reason", string(r))
	a.d.Engine.ResetNetwork()
	sel, _, err := a.d.Engine.Nodes("")
	if err != nil || sel == "" {
		return
	}
	// Give the new network a moment (DHCP/DNS) before probing.
	for attempt := 0; attempt < 3; attempt++ {
		pctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		_, err = a.d.Engine.DelayTest(pctx, sel, a.d.Settings.Get().TestTarget())
		cancel()
		if err == nil {
			return
		}
		t := a.d.Clock.NewTimer(time.Duration(attempt+1) * 2 * time.Second)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C():
		}
	}
	a.log.Warn("node unreachable after network event; reloading core", "node", sel, "err", err)
	if err := a.sup.Reload(ctx); err != nil {
		a.log.Warn("reload", "err", err)
	}
}

// viaCoreClient returns an HTTP client using the running core, or nil.
func (a *App) viaCoreClient() *http.Client {
	if st, _ := a.sup.State(); st != supervisor.Running {
		return nil
	}
	p := a.d.Settings.Get().Port()
	pu := &url.URL{Scheme: "http", Host: "127.0.0.1:" + strconv.Itoa(p)}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.Proxy = http.ProxyURL(pu)
	return &http.Client{Transport: tr, Timeout: 30 * time.Second}
}

// Status returns a snapshot for the UI.
func (a *App) Status() Status {
	s := a.d.Settings.Get()
	st, cerr := a.sup.State()
	out := Status{
		Configured: s.SubscriptionURL != "",
		Core:       st,
		Port:       s.Port(),
		UserInfo:   s.UserInfo,
		LastFetch:  s.LastFetch,
		Selected:   s.SelectedNode,
	}
	if u, err := url.Parse(s.SubscriptionURL); err == nil {
		out.PanelHost = u.Host
	}
	if cerr != nil {
		out.CoreErr = cerr.Error()
	}
	a.mu.Lock()
	out.SubErr = a.subErr
	out.SystemProxy = a.sysProxyOn
	out.SysProxyErr = a.sysProxyErr
	a.mu.Unlock()
	if st == supervisor.Running {
		if sel, nodes, err := a.d.Engine.Nodes(s.TestTarget()); err == nil {
			out.Selected, out.Nodes = sel, nodes
		}
	}
	return out
}

// Settings returns the persisted settings.
func (a *App) Settings() settings.Settings { return a.d.Settings.Get() }
