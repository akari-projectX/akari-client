package app

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/akari-projectX/akari-client/internal/clock"
	"github.com/akari-projectX/akari-client/internal/core"
	"github.com/akari-projectX/akari-client/internal/netwatch"
	"github.com/akari-projectX/akari-client/internal/settings"
	"github.com/akari-projectX/akari-client/internal/subscription"
	"github.com/akari-projectX/akari-client/internal/supervisor"
	"github.com/akari-projectX/akari-client/internal/sysproxy"
)

const tok = "AbCdEfGhIjKlMnOpQrStUvWxYz0123456789-_abcde"

type fakeEngine struct {
	mu       sync.Mutex
	running  bool
	profile  string
	port     int
	selected string
	nodes    []string
	starts   int
	resets   int
	delayErr error
}

func nodesOf(p string) []string {
	var out []string
	for _, l := range strings.Split(p, "\n") {
		if n, ok := strings.CutPrefix(strings.TrimSpace(l), "- name: "); ok {
			out = append(out, n)
		}
	}
	return out
}

func (f *fakeEngine) Start(sub []byte, o core.Options) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.starts++
	if strings.Contains(string(sub), "kernel-rejects") {
		f.running = false // like core.Engine: a refused reload stops the kernel
		return errors.New("kernel rejected config")
	}
	f.running, f.profile, f.port = true, string(sub), o.MixedPort
	f.nodes = nodesOf(f.profile)
	if len(f.nodes) > 0 {
		f.selected = f.nodes[0]
	}
	return nil
}
func (f *fakeEngine) Stop() error { f.mu.Lock(); f.running = false; f.mu.Unlock(); return nil }
func (f *fakeEngine) Healthy(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.running {
		return errors.New("down")
	}
	return nil
}
func (f *fakeEngine) Nodes(string) (string, []core.Node, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var ns []core.Node
	for _, n := range f.nodes {
		ns = append(ns, core.Node{Name: n})
	}
	return f.selected, ns, nil
}
func (f *fakeEngine) Select(n string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, x := range f.nodes {
		if x == n {
			f.selected = n
			return nil
		}
	}
	return errors.New("no such node")
}
func (f *fakeEngine) DelayTest(context.Context, string, string) (time.Duration, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return time.Millisecond, f.delayErr
}
func (f *fakeEngine) DelayTestAll(context.Context, string, time.Duration) map[string]time.Duration {
	return map[string]time.Duration{}
}
func (f *fakeEngine) ResetNetwork() { f.mu.Lock(); f.resets++; f.mu.Unlock() }

type fakeSP struct {
	mu      sync.Mutex
	enabled *sysproxy.Endpoint
	log     []string
}

func (s *fakeSP) Enable(_ context.Context, ep sysproxy.Endpoint) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.enabled = &ep
	s.log = append(s.log, "on "+ep.String())
	return nil
}
func (s *fakeSP) Disable(_ context.Context, ep sysproxy.Endpoint) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.enabled != nil && *s.enabled == ep {
		s.enabled = nil
		s.log = append(s.log, "off "+ep.String())
	}
	return nil
}
func (s *fakeSP) state() *sysproxy.Endpoint { s.mu.Lock(); defer s.mu.Unlock(); return s.enabled }

type panel struct {
	mu      sync.Mutex
	body    string
	etag    string
	status  int
	hits    int
	lastINM string
}

func (p *panel) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.hits++
	p.lastINM = r.Header.Get("If-None-Match")
	if !strings.HasSuffix(r.URL.Path, "/sub/"+tok) || !strings.Contains(r.UserAgent(), "mihomo") {
		w.WriteHeader(404)
		return
	}
	if p.status != 0 {
		w.WriteHeader(p.status)
		return
	}
	if p.etag != "" && p.lastINM == p.etag {
		w.WriteHeader(304)
		return
	}
	if p.etag != "" {
		w.Header().Set("ETag", p.etag)
	}
	w.Header().Set("Subscription-Userinfo", "upload=0; download=1; total=2; expire=0")
	_, _ = io.WriteString(w, p.body)
}

func (p *panel) set(fn func(*panel)) { p.mu.Lock(); fn(p); p.mu.Unlock() }

const prof1 = "proxies:\n  - name: hk-1\n  - name: jp-1\n"
const prof2 = "proxies:\n  - name: jp-1\n  - name: us-1\n"

func validate(b []byte) error {
	if !strings.HasPrefix(string(b), "proxies:") {
		return errors.New("not a profile")
	}
	return nil
}

type rig struct {
	app    *App
	eng    *fakeEngine
	sp     *fakeSP
	panel  *panel
	srv    *httptest.Server
	dir    string
	cancel context.CancelFunc
	done   chan struct{}
}

func newRig(t *testing.T, dir string, p *panel) *rig { return newRigClock(t, dir, p, nil) }

func newRigClock(t *testing.T, dir string, p *panel, clk clock.Clock) *rig {
	t.Helper()
	if p == nil {
		p = &panel{body: prof1, etag: `"1"`}
	}
	srv := httptest.NewServer(p)
	t.Cleanup(srv.Close)
	st, err := settings.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	r := &rig{eng: &fakeEngine{}, sp: &fakeSP{}, panel: p, srv: srv, dir: dir}
	r.app, err = New(Deps{
		Log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		Dir:      dir,
		Settings: st,
		Engine:   r.eng,
		Validate: validate,
		Fetcher:  &subscription.Fetcher{UserAgent: "akari-client/test mihomo", DeviceID: "d", Direct: subscription.NewDirectClient()},
		SysProxy: r.sp,
		Clock:    clk,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	r.cancel, r.done = cancel, make(chan struct{})
	go func() { r.app.Run(ctx); close(r.done) }()
	return r
}

func (r *rig) stop() { r.cancel(); <-r.done }

func eventually(t *testing.T, what string, f func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if f() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestLoginConnectSystemProxyAndRestore(t *testing.T) {
	dir := t.TempDir()
	r := newRig(t, dir, nil)
	ctx := context.Background()

	if err := r.app.Connect(ctx); err == nil {
		t.Fatal("connect without login succeeded")
	}
	if err := r.app.Login(ctx, r.srv.URL+"/prefix", "bad"); err == nil {
		t.Fatal("bad token accepted")
	}
	if err := r.app.Login(ctx, r.srv.URL+"/prefix/sub/"+strings.Repeat("x", 43), ""); !errors.Is(err, subscription.ErrRejected) {
		t.Fatalf("unknown token: %v", err)
	}
	if err := r.app.SetSystemProxy(ctx, true); err != nil {
		t.Fatal(err)
	}
	if err := r.app.Login(ctx, r.srv.URL+"/prefix", tok); err != nil {
		t.Fatal(err)
	}
	st := r.app.Status()
	if st.Core != supervisor.Running || !st.Configured || st.UserInfo == nil || st.UserInfo.Total != 2 {
		t.Fatalf("status: %+v", st)
	}
	eventually(t, "system proxy on", func() bool { e := r.sp.state(); return e != nil && e.Port == 7890 })
	if b, _ := os.ReadFile(filepath.Join(dir, ProfileFile)); string(b) != prof1 {
		t.Fatalf("profile not persisted: %q", b)
	}
	if err := r.app.SelectNode("jp-1"); err != nil {
		t.Fatal(err)
	}
	if err := r.app.SelectNode("nope"); err == nil {
		t.Fatal("unknown node selected")
	}

	// Quit: OS proxy reverted, preferences kept.
	r.stop()
	if r.sp.state() != nil {
		t.Fatal("system proxy left on after quit")
	}

	// Relaunch: auto-connects with the stored profile (no fetch needed),
	// restores the node and the system proxy.
	r2 := newRig(t, dir, r.panel)
	defer r2.stop()
	eventually(t, "auto-connect", func() bool { st := r2.app.Status(); return st.Core == supervisor.Running && st.Selected == "jp-1" })
	eventually(t, "system proxy restored", func() bool { return r2.sp.state() != nil })

	// Disconnect reverts the proxy and stops the core.
	if err := r2.app.Disconnect(ctx); err != nil {
		t.Fatal(err)
	}
	if r2.sp.state() != nil || r2.app.Status().Core != supervisor.Stopped {
		t.Fatal("disconnect incomplete")
	}
	if r2.app.Settings().Connect {
		t.Fatal("disconnect not persisted")
	}
}

func TestRefreshETagInvalidAndReload(t *testing.T) {
	r := newRig(t, t.TempDir(), nil)
	defer r.stop()
	ctx := context.Background()
	if err := r.app.Login(ctx, r.srv.URL+"/p/sub/"+tok, ""); err != nil {
		t.Fatal(err)
	}
	starts := func() int { r.eng.mu.Lock(); defer r.eng.mu.Unlock(); return r.eng.starts }
	if starts() != 1 {
		t.Fatalf("starts = %d", starts())
	}

	// 304: no reload, If-None-Match sent.
	if err := r.app.refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if r.panel.lastINM != `"1"` || starts() != 1 {
		t.Fatalf("INM=%q starts=%d", r.panel.lastINM, starts())
	}

	// Invalid profile: rejected, old profile kept, error surfaced.
	r.panel.set(func(p *panel) { p.body, p.etag = "<html>maintenance</html>", `"2"` })
	if err := r.app.refresh(ctx); err == nil {
		t.Fatal("invalid profile accepted")
	}
	if st := r.app.Status(); st.SubErr == "" || st.Core != supervisor.Running {
		t.Fatalf("status: %+v", st)
	}
	if b, _ := os.ReadFile(filepath.Join(r.dir, ProfileFile)); string(b) != prof1 {
		t.Fatal("invalid profile persisted")
	}

	// Panel rejects (rotated token): keep running on the old profile.
	r.panel.set(func(p *panel) { p.status = 404 })
	if err := r.app.refresh(ctx); !errors.Is(err, subscription.ErrRejected) {
		t.Fatalf("err = %v", err)
	}

	// New valid profile: persisted and the core reloaded.
	r.panel.set(func(p *panel) { p.status, p.body, p.etag = 0, prof2, `"3"` })
	if err := r.app.refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if starts() != 2 || r.app.Status().SubErr != "" {
		t.Fatalf("starts=%d status=%+v", starts(), r.app.Status())
	}
	if s := r.app.Settings(); s.ETag != `"3"` {
		t.Fatalf("etag = %q", s.ETag)
	}
	// Panel without ETag support: plain 200s still work.
	r.panel.set(func(p *panel) { p.etag = "" })
	if err := r.app.refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if r.app.Settings().ETag != "" {
		t.Fatal("stale etag kept")
	}
}

func TestSetPortMovesSystemProxy(t *testing.T) {
	r := newRig(t, t.TempDir(), nil)
	defer r.stop()
	ctx := context.Background()
	_ = r.app.SetSystemProxy(ctx, true)
	if err := r.app.Login(ctx, r.srv.URL+"/p/sub/"+tok, ""); err != nil {
		t.Fatal(err)
	}
	eventually(t, "proxy on", func() bool { return r.sp.state() != nil })
	if err := r.app.SetPort(ctx, 17890); err != nil {
		t.Fatal(err)
	}
	eventually(t, "proxy moved", func() bool { e := r.sp.state(); return e != nil && e.Port == 17890 })
	r.eng.mu.Lock()
	port := r.eng.port
	r.eng.mu.Unlock()
	if port != 17890 {
		t.Fatalf("engine port = %d", port)
	}
	if err := r.app.SetPort(ctx, 80); err == nil {
		t.Fatal("privileged port accepted")
	}
	if err := r.app.SetSystemProxy(ctx, false); err != nil || r.sp.state() != nil {
		t.Fatal("toggle off failed")
	}
}

func TestLogout(t *testing.T) {
	r := newRig(t, t.TempDir(), nil)
	defer r.stop()
	ctx := context.Background()
	if err := r.app.Login(ctx, r.srv.URL+"/p/sub/"+tok, ""); err != nil {
		t.Fatal(err)
	}
	if err := r.app.Logout(ctx); err != nil {
		t.Fatal(err)
	}
	if st := r.app.Status(); st.Configured || st.Core != supervisor.Stopped {
		t.Fatalf("status: %+v", st)
	}
	if _, err := os.Stat(filepath.Join(r.dir, ProfileFile)); !os.IsNotExist(err) {
		t.Fatal("profile kept after logout")
	}
}

func TestSelfHeal(t *testing.T) {
	clk := clock.NewFake(time.Now())
	r := newRigClock(t, t.TempDir(), nil, clk)
	defer r.stop()
	ctx := context.Background()
	if err := r.app.Login(ctx, r.srv.URL+"/p/sub/"+tok, ""); err != nil {
		t.Fatal(err)
	}
	starts := func() (int, int) { r.eng.mu.Lock(); defer r.eng.mu.Unlock(); return r.eng.starts, r.eng.resets }

	// Node reachable after the event: connections reset, no reload.
	r.app.heal(ctx, netwatch.NetworkChanged)
	if s, rs := starts(); s != 1 || rs != 1 {
		t.Fatalf("starts=%d resets=%d", s, rs)
	}

	// Node unreachable: three probes (2s, 4s, 6s apart), then reload.
	r.eng.mu.Lock()
	r.eng.delayErr = errors.New("timeout")
	r.eng.mu.Unlock()
	done := make(chan struct{})
	go func() { r.app.heal(ctx, netwatch.Resumed); close(done) }()
	// Probes wait 2s, 4s, 6s; advance in 1s steps until heal returns.
	for i := 0; ; i++ {
		select {
		case <-done:
		default:
			if i > 100 {
				t.Fatal("heal did not finish")
			}
			clk.BlockUntil(3, time.Second) // health + scheduler + probe timer
			clk.Advance(time.Second)
			continue
		}
		break
	}
	<-done
	if s, rs := starts(); s != 2 || rs != 2 {
		t.Fatalf("starts=%d resets=%d", s, rs)
	}
}

func TestRefreshRevertsWhenKernelRejects(t *testing.T) {
	r := newRig(t, t.TempDir(), nil)
	defer r.stop()
	ctx := context.Background()
	if err := r.app.Login(ctx, r.srv.URL+"/p/sub/"+tok, ""); err != nil {
		t.Fatal(err)
	}
	r.panel.set(func(p *panel) { p.body, p.etag = prof2+"# kernel-rejects\n", `"bad"` })
	if err := r.app.refresh(ctx); err == nil {
		t.Fatal("rejected profile reported as success")
	}
	st := r.app.Status()
	if st.Core != supervisor.Running || st.SubErr == "" {
		t.Fatalf("status: %+v", st)
	}
	r.eng.mu.Lock()
	running := r.eng.profile
	r.eng.mu.Unlock()
	if running != prof1 {
		t.Fatalf("kernel runs %q", running)
	}
	if b, _ := os.ReadFile(filepath.Join(r.dir, ProfileFile)); string(b) != prof1 {
		t.Fatal("rejected profile persisted")
	}
	if r.app.Settings().ETag != "" {
		t.Fatal("etag of rejected profile kept")
	}
}

func TestLoginWhileConnectedReloads(t *testing.T) {
	p := &panel{body: prof1}
	r := newRig(t, t.TempDir(), p)
	defer r.stop()
	ctx := context.Background()
	if err := r.app.Login(ctx, r.srv.URL+"/a/sub/"+tok, ""); err != nil {
		t.Fatal(err)
	}
	p.set(func(p *panel) { p.body = prof2 })
	if err := r.app.Login(ctx, r.srv.URL+"/b/sub/"+tok, ""); err != nil {
		t.Fatal(err)
	}
	r.eng.mu.Lock()
	defer r.eng.mu.Unlock()
	if r.eng.profile != prof2 || r.eng.starts != 2 {
		t.Fatalf("profile=%q starts=%d", r.eng.profile, r.eng.starts)
	}
}
