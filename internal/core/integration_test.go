//go:build !race

// The integration test is excluded from -race builds: mihomo v1.19.31
// itself has unsynchronized globals written by executor.ApplyConfig and
// read by live listener goroutines (adapter/inbound ipfilter). Our reload
// path hits it by design; see docs/DECISIONS.md D1 "known upstream races".
// `make test` runs this file without -race.

package core

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

// Integration: the real embedded mihomo proxies an HTTP request to a local
// server through its mixed port, switches nodes, measures delay, stops
// (port closed) and restarts.
func TestEngineIntegration(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/generate_204" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		_, _ = io.WriteString(w, "hello via mihomo")
	}))
	defer srv.Close()

	e := Default(t.TempDir())
	port := freePort(t)
	if err := e.Start([]byte(directProfile), Options{MixedPort: port}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.Stop() })

	ctx := context.Background()
	if err := e.Healthy(ctx); err != nil {
		t.Fatalf("healthy: %v", err)
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
	if Connections() < 0 {
		t.Fatal("unreachable")
	}

	sel, nodes, err := e.Nodes(srv.URL + "/generate_204")
	if err != nil || sel != "node-a" || len(nodes) != 2 {
		t.Fatalf("nodes: sel=%q nodes=%v err=%v", sel, nodes, err)
	}
	if err := e.Select("node-b"); err != nil {
		t.Fatal(err)
	}
	if err := e.Select("nope"); err == nil {
		t.Fatal("selecting unknown node should fail")
	}
	d, err := e.DelayTest(ctx, "node-b", srv.URL+"/generate_204")
	if err != nil || d <= 0 {
		t.Fatalf("delay: %v %v", d, err)
	}
	all := e.DelayTestAll(ctx, srv.URL+"/generate_204", 5*time.Second)
	if len(all) != 2 || all["node-a"] <= 0 {
		t.Fatalf("delay all: %v", all)
	}
	_, nodes, _ = e.Nodes(srv.URL + "/generate_204")
	if nodes[1].DelayMS <= 0 {
		t.Fatalf("delay not recorded: %+v", nodes)
	}
	sel, _, _ = e.Nodes(srv.URL + "/generate_204")
	if sel != "node-b" {
		t.Fatalf("selection lost: %q", sel)
	}

	// Reload in place keeps serving.
	if err := e.Start([]byte(directProfile), Options{MixedPort: port}); err != nil {
		t.Fatal(err)
	}
	_ = get()

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

	// Port in use -> Start fails cleanly.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	busy := l.Addr().(*net.TCPAddr).Port
	if err := e.Start([]byte(directProfile), Options{MixedPort: busy}); err == nil {
		t.Fatal("expected port-in-use error")
	}
	l.Close()
	if e.Running() {
		t.Fatal("running after failed start")
	}

	// Restart after stop.
	if err := e.Start([]byte(directProfile), Options{MixedPort: port}); err != nil {
		t.Fatal(err)
	}
	if got := get(); got != "hello via mihomo" {
		t.Fatalf("after restart: %q", got)
	}
}
