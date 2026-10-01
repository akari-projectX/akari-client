package core

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// kernel returns the mihomo binary for integration tests: $AKARI_MIHOMO_BIN
// or bin/mihomo (`make mihomo`). Missing binary = skip, unless
// AKARI_REQUIRE_MIHOMO=1 (CI).
func kernel(t *testing.T) string {
	t.Helper()
	p := os.Getenv("AKARI_MIHOMO_BIN")
	if p == "" {
		p, _ = filepath.Abs(filepath.Join("..", "..", "bin", BinaryName()))
	}
	if _, err := os.Stat(p); err != nil {
		if os.Getenv("AKARI_REQUIRE_MIHOMO") == "1" {
			t.Fatalf("mihomo binary required: %v", err)
		}
		t.Skipf("mihomo binary not found (%s); run `make mihomo`", p)
	}
	return p
}

func freePort(t *testing.T) int {
	t.Helper()
	p, err := freeLoopbackPort()
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func newTestEngine(t *testing.T, home string) *Engine {
	t.Helper()
	e, err := NewEngine(kernel(t), home, slog.New(slog.NewTextHandler(testWriter{t}, nil)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.Stop() })
	return e
}

func TestKernelVersionPinned(t *testing.T) {
	v, err := BinaryVersion(context.Background(), kernel(t))
	if err != nil {
		t.Fatal(err)
	}
	if v != MihomoVersion {
		t.Fatalf("kernel %s, want %s", v, MihomoVersion)
	}
}

func TestKernelValidate(t *testing.T) {
	e := newTestEngine(t, t.TempDir())
	if err := e.Validate([]byte(panelProfile)); err != nil {
		t.Fatalf("panel profile: %v", err)
	}
	// Passes our structural checks, rejected by the kernel.
	bad := strings.Replace(panelProfile, "    uuid: 2b7c1c43-6d2e-4c4f-9a59-5b8f6f3b8a11\n", "", 1)
	err := e.Validate([]byte(bad))
	if err == nil || !strings.Contains(err.Error(), "rejected by kernel") {
		t.Fatalf("missing uuid: %v", err)
	}
	t.Logf("kernel says: %v", err)
}

// End to end: the real kernel proxies HTTP (and SOCKS5) to a local server,
// switches nodes, measures delay, reloads in place, survives a crash
// (detected by Healthy), stops (port closed), reports a busy port and
// cleans up an orphan left by a dead client.
func TestEngineIntegration(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/generate_204" {
			// mihomo's API reports a 0 ms result as a failure; loopback
			// is sub-millisecond, so add a little latency.
			time.Sleep(3 * time.Millisecond)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		_, _ = io.WriteString(w, "hello via mihomo")
	}))
	defer srv.Close()
	home := t.TempDir()
	e := newTestEngine(t, home)
	port := freePort(t)
	ctx := context.Background()

	if err := e.Start([]byte(directProfile), Options{MixedPort: port}); err != nil {
		t.Fatal(err)
	}
	if err := e.Healthy(ctx); err != nil {
		t.Fatalf("healthy: %v", err)
	}
	if fi, err := os.Stat(filepath.Join(home, configFile)); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("config file mode: %v %v", fi.Mode().Perm(), err)
	}

	proxyURL, _ := url.Parse(fmt.Sprintf("http://127.0.0.1:%d", port))
	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)}, Timeout: 10 * time.Second}
	get := func() string {
		t.Helper()
		resp, err := client.Get(srv.URL + "/hello")
		if err != nil {
			t.Fatalf("proxied GET: %v", err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return string(b)
	}
	if got := get(); got != "hello via mihomo" {
		t.Fatalf("body = %q", got)
	}

	test := srv.URL + "/generate_204"
	sel, nodes, err := e.Nodes(test)
	if err != nil || sel != "node-a" || len(nodes) != 2 || nodes[1].Name != "node-b" {
		t.Fatalf("nodes: sel=%q nodes=%v err=%v", sel, nodes, err)
	}
	if err := e.Select("node-b"); err != nil {
		t.Fatal(err)
	}
	if err := e.Select("nope"); err == nil {
		t.Fatal("selecting unknown node should fail")
	}
	if d, err := e.DelayTest(ctx, "node-b", test); err != nil || d <= 0 {
		t.Fatalf("delay: %v %v", d, err)
	}
	short, cancel := context.WithTimeout(ctx, time.Second)
	if _, err := e.DelayTest(short, "node-b", "http://127.0.0.1:1/unreachable"); err == nil {
		t.Fatal("delay to closed port succeeded")
	}
	cancel()
	all := e.DelayTestAll(ctx, test, 5*time.Second)
	if len(all) != 2 || all["node-a"] <= 0 || all["node-b"] <= 0 {
		t.Fatalf("delay all: %v", all)
	}
	sel, nodes, _ = e.Nodes(test)
	if sel != "node-b" || nodes[0].DelayMS <= 0 {
		t.Fatalf("after tests: sel=%q nodes=%+v", sel, nodes)
	}
	e.ResetNetwork()

	// In-place reload (same process) with a changed profile and port.
	pid := e.proc.cmd.Process.Pid
	port2 := freePort(t)
	prof2 := strings.Replace(directProfile, `"node-b"`, `"node-c"`, 2)
	if err := e.Start([]byte(prof2), Options{MixedPort: port2}); err != nil {
		t.Fatal(err)
	}
	if e.proc.cmd.Process.Pid != pid {
		t.Fatal("reload restarted the process")
	}
	if _, nodes, _ := e.Nodes(test); nodes[1].Name != "node-c" {
		t.Fatalf("reload not applied: %+v", nodes)
	}
	proxyURL.Host = "127.0.0.1:" + strconv.Itoa(port2)
	if got := get(); got != "hello via mihomo" {
		t.Fatalf("after reload: %q", got)
	}
	// Validation while running uses its own directory (no cache-file
	// contention with the live kernel).
	if err := e.Validate([]byte(panelProfile)); err != nil {
		t.Fatalf("validate while running: %v", err)
	}
	// Invalid profile on reload: rejected and the kernel is stopped (the
	// app then restarts it with the previous profile).
	if err := e.Start([]byte(strings.Replace(panelProfile, "uuid:", "uuidx:", 1)), Options{MixedPort: port2}); err == nil {
		t.Fatal("bad reload accepted")
	}
	if e.Running() {
		t.Fatal("kernel left running after rejected reload")
	}
	if err := e.Start([]byte(prof2), Options{MixedPort: port2}); err != nil {
		t.Fatal(err)
	}

	// Crash: Healthy reports it; Start spawns a fresh kernel.
	_ = e.proc.cmd.Process.Kill()
	<-e.proc.done
	if err := e.Healthy(ctx); err == nil {
		t.Fatal("crash not detected")
	}
	if err := e.Start([]byte(directProfile), Options{MixedPort: port}); err != nil {
		t.Fatal(err)
	}
	proxyURL.Host = "127.0.0.1:" + strconv.Itoa(port)
	if got := get(); got != "hello via mihomo" {
		t.Fatalf("after crash restart: %q", got)
	}

	// Stop closes the port.
	if err := e.Stop(); err != nil {
		t.Fatal(err)
	}
	if e.Running() || e.Healthy(ctx) == nil {
		t.Fatal("still running after stop")
	}
	if c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), time.Second); err == nil {
		c.Close()
		t.Fatal("mixed port still open after stop")
	}

	// Busy port: clean error, nothing left running.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	busy := l.Addr().(*net.TCPAddr).Port
	err = e.Start([]byte(directProfile), Options{MixedPort: busy})
	l.Close()
	if err == nil || !strings.Contains(err.Error(), "port in use") {
		t.Fatalf("busy port: %v", err)
	}
	if e.Running() {
		t.Fatal("running after failed start")
	}

	// Orphan: a kernel left behind by a dead client (simulated by a second
	// engine on the same home dir that never stops its process) is killed
	// on the next start, freeing the port.
	orphanEng, _ := NewEngine(e.bin, home, e.log)
	if err := orphanEng.Start([]byte(directProfile), Options{MixedPort: port}); err != nil {
		t.Fatal(err)
	}
	orphan := orphanEng.proc
	if err := e.Start([]byte(directProfile), Options{MixedPort: port}); err != nil {
		t.Fatalf("start over orphan: %v", err)
	}
	select {
	case <-orphan.done:
	case <-time.After(5 * time.Second):
		t.Fatal("orphan not killed")
	}
	if got := get(); got != "hello via mihomo" {
		t.Fatalf("after orphan cleanup: %q", got)
	}
}

type testWriter struct{ t *testing.T }

func (w testWriter) Write(p []byte) (int, error) {
	if testing.Verbose() {
		w.t.Log(strings.TrimRight(string(p), "\n"))
	}
	return len(p), nil
}
