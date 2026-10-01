package subscription

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/akari-projectX/akari-client/internal/clock"
)

const tok = "AbCdEfGhIjKlMnOpQrStUvWxYz0123456789-_abcde" // 43 chars

func TestNormalize(t *testing.T) {
	ok := map[[2]string]string{
		{"https://p.example.com/s3cr3t/sub/" + tok, ""}:          "https://p.example.com/s3cr3t/sub/" + tok,
		{" https://p.example.com/s3cr3t/sub/" + tok + "#x ", ""}: "https://p.example.com/s3cr3t/sub/" + tok,
		{"https://p.example.com/s3cr3t/sub/" + tok, tok}:         "https://p.example.com/s3cr3t/sub/" + tok,
		{"https://p.example.com/s3cr3t", tok}:                    "https://p.example.com/s3cr3t/sub/" + tok,
		{"https://p.example.com/s3cr3t/", tok}:                   "https://p.example.com/s3cr3t/sub/" + tok,
		{"https://p.example.com/s3cr3t/app", tok}:                "https://p.example.com/s3cr3t/sub/" + tok,
		{"https://p.example.com:8443/a/b?x=1", tok}:              "https://p.example.com:8443/a/b/sub/" + tok,
		{"http://127.0.0.1:8080/test", tok}:                      "http://127.0.0.1:8080/test/sub/" + tok,
		{"http://localhost/test/sub/" + tok, ""}:                 "http://localhost/test/sub/" + tok,
	}
	for in, want := range ok {
		got, err := Normalize(in[0], in[1])
		if err != nil || got != want {
			t.Errorf("Normalize(%q,%q) = %q, %v; want %q", in[0], in[1], got, err, want)
		}
	}
	bad := [][2]string{
		{"", tok},
		{"http://p.example.com/s/sub/" + tok, ""}, // plain http, remote
		{"ftp://p.example.com/s", tok},            // scheme
		{"https://u:p@p.example.com/s", tok},      // credentials
		{"https://p.example.com/s", ""},           // no token
		{"https://p.example.com/", tok},           // no prefix
		{"https://p.example.com/sub/" + tok, ""},  // no prefix
		{"https://p.example.com/s/sub/short", ""}, // malformed token
		{"https://p.example.com/s", "bad token!"}, // malformed token
		{"https://p.example.com/s/sub/" + tok, "AbCdEfGhIjKlMnOpQrStUvWxYz0123456789-_zzzzz"},
		{"p.example.com/s", tok}, // no scheme
	}
	for _, in := range bad {
		if got, err := Normalize(in[0], in[1]); err == nil {
			t.Errorf("Normalize(%q,%q) = %q, want error", in[0], in[1], got)
		}
	}
}

func TestRedact(t *testing.T) {
	r := Redact("https://p.example.com/s3cr3t/sub/" + tok)
	if strings.Contains(r, "s3cr3t") || strings.Contains(r, tok) || r != "https://p.example.com/…" {
		t.Fatalf("Redact = %q", r)
	}
}

func TestParseUserInfo(t *testing.T) {
	u := ParseUserInfo("upload=0; download=1234; total=5000; expire=1767225600")
	if u == nil || u.Download != 1234 || u.Total != 5000 || u.Expire != 1767225600 {
		t.Fatalf("%+v", u)
	}
	if ParseUserInfo("") != nil || ParseUserInfo("garbage") != nil {
		t.Fatal("expected nil")
	}
}

func newFetcher() *Fetcher {
	return &Fetcher{UserAgent: "akari-client/test mihomo", DeviceID: "dev-1", Direct: NewDirectClient()}
}

func TestFetchHeadersETagAndErrors(t *testing.T) {
	var mu sync.Mutex
	var seen []*http.Request
	status := http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r)
		st := status
		mu.Unlock()
		if r.Header.Get("If-None-Match") == `"v1"` {
			w.Header().Set("Subscription-Userinfo", "upload=0; download=9; total=10; expire=0")
			w.WriteHeader(http.StatusNotModified)
			return
		}
		if st != http.StatusOK {
			w.WriteHeader(st)
			return
		}
		w.Header().Set("ETag", `"v1"`)
		w.Header().Set("Subscription-Userinfo", "upload=0; download=1; total=10; expire=0")
		w.Header().Set("Profile-Update-Interval", "24")
		_, _ = io.WriteString(w, "proxies: []\n"+strings.Repeat("\n", 100))
	}))
	defer srv.Close()
	f := newFetcher()
	ctx := context.Background()

	r, err := f.Fetch(ctx, srv.URL+"/s/sub/"+tok, "")
	if err != nil || r.NotModified || r.ETag != `"v1"` || r.IntervalHours != 24 || r.UserInfo.Download != 1 {
		t.Fatalf("first fetch: %+v %v", r, err)
	}
	req := seen[0]
	if req.Header.Get("User-Agent") != "akari-client/test mihomo" || req.Header.Get("X-Akari-Device") != "dev-1" || req.Header.Get("If-None-Match") != "" {
		t.Fatalf("request headers: %v", req.Header)
	}
	r, err = f.Fetch(ctx, srv.URL+"/s/sub/"+tok, `"v1"`)
	if err != nil || !r.NotModified || r.ETag != `"v1"` || r.UserInfo.Download != 9 || r.Body != nil {
		t.Fatalf("conditional fetch: %+v %v", r, err)
	}

	mu.Lock()
	status = http.StatusNotFound
	mu.Unlock()
	if _, err := f.Fetch(ctx, srv.URL+"/s/sub/"+tok, ""); !errors.Is(err, ErrRejected) {
		t.Fatalf("404 = %v", err)
	}
	mu.Lock()
	status = http.StatusBadGateway
	mu.Unlock()
	if _, err := f.Fetch(ctx, srv.URL+"/s/sub/"+tok, ""); err == nil || !strings.Contains(err.Error(), "502") {
		t.Fatalf("502 = %v", err)
	}
}

func TestFetchLimitsAndEmpty(t *testing.T) {
	big := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if big {
			_, _ = w.Write(make([]byte, MaxBodyBytes+10))
			return
		}
		_, _ = io.WriteString(w, "\n\n  \n")
	}))
	defer srv.Close()
	f := newFetcher()
	if _, err := f.Fetch(context.Background(), srv.URL, ""); err == nil {
		t.Fatal("empty body accepted")
	}
	big = true
	if _, err := f.Fetch(context.Background(), srv.URL, ""); err == nil {
		t.Fatal("oversized body accepted")
	}
}

func TestFetchErrorDoesNotLeakURL(t *testing.T) {
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := l.Addr().String()
	l.Close()
	_, err := newFetcher().Fetch(context.Background(), "http://"+addr+"/secretprefix/sub/"+tok, "")
	if err == nil || strings.Contains(err.Error(), tok) || strings.Contains(err.Error(), "secretprefix") {
		t.Fatalf("err = %v", err)
	}
}

func TestFetchFallsBackViaCore(t *testing.T) {
	// "Core" = an HTTP proxy that serves the profile for any request.
	var via atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		via.Add(1)
		_, _ = io.WriteString(w, "proxies: []\n")
	}))
	defer proxy.Close()
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	dead := l.Addr().String()
	l.Close()
	f := newFetcher()
	f.ViaCore = func() *http.Client {
		return &http.Client{Transport: &http.Transport{Proxy: func(*http.Request) (*neturlURL, error) { return parseURL(proxy.URL) }}}
	}
	r, err := f.Fetch(context.Background(), "http://"+dead+"/s/sub/"+tok, "")
	if err != nil || via.Load() != 1 || len(r.Body) == 0 {
		t.Fatalf("fallback: %v via=%d", err, via.Load())
	}
	// HTTP-level rejection does not fall back.
	f.Direct = &http.Client{Transport: &http.Transport{Proxy: func(*http.Request) (*neturlURL, error) { return parseURL(proxy.URL) }}}
	f.ViaCore = func() *http.Client { t.Fatal("fallback used after HTTP response"); return nil }
	if _, err := f.Fetch(context.Background(), "http://example.invalid/s/sub/"+tok, ""); err != nil {
		t.Fatal(err)
	}
}

func TestNextDelay(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	iv := 12 * time.Hour
	cases := []struct {
		last     time.Time
		failures int
		want     time.Duration
	}{
		{time.Time{}, 0, 0},
		{now.Add(-time.Hour), 0, 11 * time.Hour},
		{now.Add(-13 * time.Hour), 0, 0},
		{now.Add(time.Hour), 0, iv}, // clock went backwards
		{now, 1, time.Minute},
		{now, 3, 4 * time.Minute},
		{now, 10, 30 * time.Minute},
	}
	for _, c := range cases {
		if got := NextDelay(now, c.last, iv, c.failures); got != c.want {
			t.Errorf("NextDelay(last=%v, f=%d) = %v, want %v", c.last.Sub(now), c.failures, got, c.want)
		}
	}
	if got := NextDelay(now, now, 15*time.Minute, 10); got != 15*time.Minute {
		t.Errorf("retry capped by interval: %v", got)
	}
}

func TestSchedulerPeriodicRetryAndTrigger(t *testing.T) {
	clk := clock.NewFake(time.Unix(1_700_000_000, 0))
	var mu sync.Mutex
	last := clk.Now().Add(-time.Hour)
	fail := false
	calls := make(chan struct{}, 10)
	s := NewScheduler(clk, func() time.Duration { return 6 * time.Hour },
		func() time.Time { mu.Lock(); defer mu.Unlock(); return last },
		func(context.Context) error {
			mu.Lock()
			defer mu.Unlock()
			calls <- struct{}{}
			if fail {
				return errors.New("down")
			}
			last = clk.Now()
			return nil
		})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()
	defer func() { cancel(); <-done }()

	expect := func(want time.Duration) {
		t.Helper()
		if !clk.BlockUntil(1, 2*time.Second) {
			t.Fatal("no timer")
		}
		if got, _ := clk.NextDeadline(); got != want {
			t.Fatalf("next = %v, want %v", got, want)
		}
	}
	called := func() {
		t.Helper()
		select {
		case <-calls:
		case <-time.After(2 * time.Second):
			t.Fatal("Do not called")
		}
	}
	expect(5 * time.Hour)
	clk.Advance(5 * time.Hour)
	called()
	expect(6 * time.Hour)

	mu.Lock()
	fail = true
	mu.Unlock()
	s.Trigger()
	called()
	expect(time.Minute)
	clk.Advance(time.Minute)
	called()
	expect(2 * time.Minute)

	mu.Lock()
	fail = false
	mu.Unlock()
	clk.Advance(2 * time.Minute)
	called()
	expect(6 * time.Hour)
}

type neturlURL = url.URL

func parseURL(s string) (*url.URL, error) { return url.Parse(s) }
