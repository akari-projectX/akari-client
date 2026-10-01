// Package core is the ONLY package that imports mihomo. mihomo's Go API is
// not a stable contract (see docs/DECISIONS.md D1): every call into it is
// funnelled through this file set so a version bump touches one place.
//
// Pinned: github.com/metacubex/mihomo v1.19.31.
//
// mihomo keeps its runtime in package-level globals (tunnel, listeners,
// resolver), so there is exactly one Engine per process (Default).
package core

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/metacubex/mihomo/adapter/outboundgroup"
	"github.com/metacubex/mihomo/component/resolver"
	"github.com/metacubex/mihomo/config"
	C "github.com/metacubex/mihomo/constant"
	"github.com/metacubex/mihomo/hub/executor"
	"github.com/metacubex/mihomo/listener"
	mlog "github.com/metacubex/mihomo/log"
	"github.com/metacubex/mihomo/tunnel"
	"github.com/metacubex/mihomo/tunnel/statistic"
	"github.com/sirupsen/logrus"
)

// MihomoVersion is the pinned kernel version (keep in sync with go.mod).
const MihomoVersion = "v1.19.31"

// PreferredGroup is the selector the panel renders (render_clash).
const PreferredGroup = "PROXY"

// Options are the client-controlled runtime settings. Everything else in
// the generated mihomo config is fixed by BuildConfig.
type Options struct {
	MixedPort int
}

// Node is one selectable outbound of the main group.
type Node struct {
	Name string
	Type string
	// DelayMS is the last delay-test result for the test URL; 0 = untested,
	// -1 = failed.
	DelayMS int
}

// Profile is a validated, parsed subscription.
type Profile struct {
	cfg   *config.Config
	Group string
	Nodes []string
}

var (
	engineOnce sync.Once
	engine     *Engine
)

// Engine controls the in-process mihomo kernel.
type Engine struct {
	mu      sync.Mutex
	running bool
	port    int
	group   string
}

// Default returns the process-wide engine. homeDir is mihomo's working
// directory (cache files); only the first call's value is used.
func Default(homeDir string) *Engine {
	engineOnce.Do(func() {
		C.SetHomeDir(homeDir)
		engine = &Engine{}
	})
	return engine
}

// ForwardLogs routes mihomo's log stream into logger and silences its
// default stdout output. Call once.
func ForwardLogs(logger *slog.Logger) {
	logrus.SetOutput(io.Discard)
	sub := mlog.Subscribe()
	go func() {
		for ev := range sub {
			lvl := slog.LevelInfo
			switch ev.LogLevel {
			case mlog.DEBUG:
				lvl = slog.LevelDebug
			case mlog.WARNING:
				lvl = slog.LevelWarn
			case mlog.ERROR:
				lvl = slog.LevelError
			case mlog.SILENT:
				continue
			}
			logger.Log(context.Background(), lvl, ev.Payload, "src", "mihomo")
		}
	}()
}

// SetLogLevel sets mihomo's log level ("debug", "info", "warning", "error", "silent").
func SetLogLevel(level string) {
	if l, ok := mlog.LogLevelMapping[level]; ok {
		mlog.SetLevel(l)
	}
}

// BuildConfig turns a panel subscription into a mihomo config.
//
// Only proxies, proxy-groups and rules are taken from the subscription;
// every other key (listeners, external controller, TUN, DNS, providers,
// authentication, allow-lan, ...) is discarded and replaced by fixed
// client values. A compromised or misconfigured panel therefore cannot
// open a LAN listener or a control API on the user's machine.
func BuildConfig(subscription []byte, opts Options) (*Profile, error) {
	if opts.MixedPort < 0 || opts.MixedPort > 65535 {
		return nil, fmt.Errorf("invalid mixed port %d", opts.MixedPort)
	}
	src, err := config.UnmarshalRawConfig(subscription)
	if err != nil {
		return nil, fmt.Errorf("subscription is not a valid Clash profile: %w", err)
	}
	if len(src.Proxy) == 0 {
		return nil, errors.New("subscription contains no proxies (no nodes assigned to this account?)")
	}
	raw := config.DefaultRawConfig()
	raw.Proxy = src.Proxy
	raw.ProxyGroup = src.ProxyGroup
	raw.Rule = src.Rule

	raw.MixedPort = opts.MixedPort
	raw.Port, raw.SocksPort, raw.RedirPort, raw.TProxyPort = 0, 0, 0, 0
	raw.AllowLan = false
	raw.BindAddress = "127.0.0.1"
	raw.Mode = tunnel.Rule
	raw.UnifiedDelay = true
	raw.Profile.StoreSelected = false // selection is persisted by the client
	raw.GeoAutoUpdate = false
	raw.ExternalUIURL = ""
	raw.Experimental.QUICGoDisableECN = true

	group := pickGroup(raw.ProxyGroup)
	if len(raw.Rule) == 0 {
		target := group
		if target == "" {
			target = "GLOBAL"
		}
		raw.Rule = []string{"MATCH," + target}
	}

	cfg, err := parseRaw(raw)
	if err != nil {
		return nil, fmt.Errorf("invalid profile: %w", err)
	}
	p := &Profile{cfg: cfg, Group: group}
	if p.Group == "" {
		p.Group = "GLOBAL"
	}
	if g, ok := cfg.Proxies[p.Group]; ok {
		if pg, ok := g.Adapter().(outboundgroup.ProxyGroup); ok {
			for _, n := range pg.Proxies() {
				p.Nodes = append(p.Nodes, n.Name())
			}
		}
	}
	return p, nil
}

// Validate parses a subscription without applying it.
func Validate(subscription []byte) error {
	_, err := BuildConfig(subscription, Options{MixedPort: 0})
	return err
}

func pickGroup(groups []map[string]any) string {
	first := ""
	for _, g := range groups {
		name, _ := g["name"].(string)
		typ, _ := g["type"].(string)
		if typ != "select" || name == "" {
			continue
		}
		if name == PreferredGroup {
			return name
		}
		if first == "" {
			first = name
		}
	}
	return first
}

func parseRaw(raw *config.RawConfig) (cfg *config.Config, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("mihomo panic while parsing: %v", r)
		}
	}()
	return config.ParseRawConfig(raw)
}

func apply(cfg *config.Config) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("mihomo panic while applying config: %v", r)
		}
	}()
	executor.ApplyConfig(cfg, true)
	return nil
}

// Start applies the subscription and opens the mixed listener on
// 127.0.0.1:opts.MixedPort. Calling Start while running reloads in place.
func (e *Engine) Start(subscription []byte, opts Options) error {
	if opts.MixedPort <= 0 {
		return fmt.Errorf("invalid mixed port %d", opts.MixedPort)
	}
	p, err := BuildConfig(subscription, opts)
	if err != nil {
		return err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := apply(p.cfg); err != nil {
		return err
	}
	if got := listener.GetPorts().MixedPort; got != opts.MixedPort {
		_ = e.stopLocked()
		return fmt.Errorf("could not listen on 127.0.0.1:%d (port in use?)", opts.MixedPort)
	}
	e.running = true
	e.port = opts.MixedPort
	e.group = p.Group
	return nil
}

// Stop closes the listener and all proxied connections.
func (e *Engine) Stop() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.stopLocked()
}

func (e *Engine) stopLocked() error {
	raw := config.DefaultRawConfig()
	raw.Profile.StoreSelected = false
	raw.ExternalUIURL = ""
	raw.Rule = []string{"MATCH,REJECT"}
	cfg, err := parseRaw(raw)
	if err != nil {
		return err
	}
	err = apply(cfg)
	closeAllConnections()
	e.running = false
	return err
}

// Running reports whether the engine has been started.
func (e *Engine) Running() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.running
}

// Port returns the active mixed port (0 when stopped).
func (e *Engine) Port() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.running {
		return 0
	}
	return e.port
}

// Healthy checks that the mixed listener is up and answering a SOCKS5
// greeting.
func (e *Engine) Healthy(ctx context.Context) error {
	e.mu.Lock()
	running, port := e.running, e.port
	e.mu.Unlock()
	if !running {
		return errors.New("core not running")
	}
	if got := listener.GetPorts().MixedPort; got != port {
		return fmt.Errorf("mixed listener missing (have port %d, want %d)", got, port)
	}
	return ProbeSOCKS(ctx, net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
}

// ProbeSOCKS performs a no-auth SOCKS5 greeting against addr.
func ProbeSOCKS(ctx context.Context, addr string) error {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	var d net.Dialer
	c, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("dial mixed port: %w", err)
	}
	defer c.Close()
	if dl, ok := ctx.Deadline(); ok {
		_ = c.SetDeadline(dl)
	}
	if _, err := c.Write([]byte{5, 1, 0}); err != nil {
		return fmt.Errorf("socks greeting: %w", err)
	}
	var resp [2]byte
	if _, err := io.ReadFull(c, resp[:]); err != nil {
		return fmt.Errorf("socks greeting: %w", err)
	}
	if resp[0] != 5 || resp[1] != 0 {
		return fmt.Errorf("unexpected socks reply %x", resp)
	}
	return nil
}

func (e *Engine) mainGroup() (outboundgroup.ProxyGroup, error) {
	e.mu.Lock()
	running, name := e.running, e.group
	e.mu.Unlock()
	if !running {
		return nil, errors.New("core not running")
	}
	p, ok := tunnel.Proxies()[name]
	if !ok {
		return nil, fmt.Errorf("group %q not found", name)
	}
	g, ok := p.Adapter().(outboundgroup.ProxyGroup)
	if !ok {
		return nil, fmt.Errorf("%q is not a group", name)
	}
	return g, nil
}

// Nodes lists the main group's members, its current selection and the
// last delay to testURL of each.
func (e *Engine) Nodes(testURL string) (selected string, nodes []Node, err error) {
	g, err := e.mainGroup()
	if err != nil {
		return "", nil, err
	}
	for _, p := range g.Proxies() {
		n := Node{Name: p.Name(), Type: p.Type().String()}
		if hist, ok := p.ExtraDelayHistories()[testURL]; ok && len(hist.History) > 0 {
			last := hist.History[len(hist.History)-1]
			switch {
			case !hist.Alive || last.Delay == 0 && last.Time.IsZero():
				n.DelayMS = -1
			default:
				n.DelayMS = max(int(last.Delay), 1) // sub-millisecond (loopback) shows as 1
			}
		}
		nodes = append(nodes, n)
	}
	return g.Now(), nodes, nil
}

// Select switches the main group to node and drops connections that were
// using the previous node so the change takes effect immediately.
func (e *Engine) Select(node string) error {
	g, err := e.mainGroup()
	if err != nil {
		return err
	}
	s, ok := g.(outboundgroup.SelectAble)
	if !ok {
		return errors.New("main group is not selectable")
	}
	prev := g.Now()
	if err := s.Set(node); err != nil {
		return fmt.Errorf("select %q: %w", node, err)
	}
	if prev != node {
		closeAllConnections()
	}
	return nil
}

// DelayTest measures one node against testURL (mihomo URL test).
func (e *Engine) DelayTest(ctx context.Context, node, testURL string) (time.Duration, error) {
	if !e.Running() {
		return 0, errors.New("core not running")
	}
	p, ok := tunnel.Proxies()[node]
	if !ok {
		return 0, fmt.Errorf("node %q not found", node)
	}
	ms, err := p.URLTest(ctx, testURL, nil)
	if err != nil {
		return 0, err
	}
	return time.Duration(max(ms, 1)) * time.Millisecond, nil
}

// DelayTestAll tests every node of the main group concurrently.
func (e *Engine) DelayTestAll(ctx context.Context, testURL string, timeout time.Duration) map[string]time.Duration {
	_, nodes, err := e.Nodes(testURL)
	out := map[string]time.Duration{}
	if err != nil {
		return out
	}
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)
	for _, n := range nodes {
		wg.Add(1)
		go func(name string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			c, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()
			d, err := e.DelayTest(c, name, testURL)
			if err != nil {
				d = -1
			}
			mu.Lock()
			out[name] = d
			mu.Unlock()
		}(n.Name)
	}
	wg.Wait()
	return out
}

// ResetNetwork drops all proxied connections and cached resolver
// connections — used after a network change or resume from sleep, when
// existing sockets are likely dead.
func (e *Engine) ResetNetwork() {
	closeAllConnections()
	resolver.ResetConnection()
}

// Connections returns the number of active proxied connections.
func Connections() int {
	n := 0
	statistic.DefaultManager.Range(func(statistic.Tracker) bool { n++; return true })
	return n
}

func closeAllConnections() {
	var ts []statistic.Tracker
	statistic.DefaultManager.Range(func(t statistic.Tracker) bool { ts = append(ts, t); return true })
	for _, t := range ts {
		_ = t.Close()
	}
}
