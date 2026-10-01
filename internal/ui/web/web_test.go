package web

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/akari-projectX/akari-client/internal/app"
	"github.com/akari-projectX/akari-client/internal/settings"
)

type fakeCtl struct {
	mu      sync.Mutex
	logins  [][2]string
	port    int
	refresh int
}

func (f *fakeCtl) Status() app.Status {
	return app.Status{Configured: true, PanelHost: "p.example.com", Port: 7890, UserInfo: &settings.UserInfo{Download: 3 << 30, Total: 100 << 30}}
}
func (f *fakeCtl) Settings() settings.Settings { return settings.Settings{} }
func (f *fakeCtl) Login(_ context.Context, u, tk string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.logins = append(f.logins, [2]string{u, tk})
	if u == "bad" {
		return errors.New("invalid <url>")
	}
	return nil
}
func (f *fakeCtl) Logout(context.Context) error { return nil }
func (f *fakeCtl) SetPort(_ context.Context, p int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.port = p
	return nil
}
func (f *fakeCtl) SetRefreshMinutes(m int) error {
	f.mu.Lock()
	f.refresh = m
	f.mu.Unlock()
	return nil
}
func (f *fakeCtl) RefreshNow() {}

func TestSettingsPage(t *testing.T) {
	ctl := &fakeCtl{}
	s := New(ctl, slog.New(slog.NewTextHandler(io.Discard, nil)))
	page, err := s.URL()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	noRedirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }, Transport: &http.Transport{Proxy: nil}}

	resp, err := noRedirect.Get(page)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(body), "p.example.com") || !strings.Contains(string(body), "3.0 GiB / 100.0 GiB") {
		t.Fatalf("page: %d %s", resp.StatusCode, body)
	}
	if resp.Header.Get("Referrer-Policy") != "no-referrer" || !strings.Contains(resp.Header.Get("Content-Security-Policy"), "default-src 'none'") {
		t.Fatal("security headers missing")
	}

	u, _ := url.Parse(page)
	// Wrong secret.
	if r, _ := noRedirect.Get(u.Scheme + "://" + u.Host + "/wrong/"); r.StatusCode != 404 {
		t.Fatalf("wrong secret: %d", r.StatusCode)
	}
	// DNS rebinding: foreign Host header.
	req, _ := http.NewRequest("GET", page, nil)
	req.Host = "evil.example:" + u.Port()
	if r, _ := noRedirect.Do(req); r.StatusCode != 403 {
		t.Fatalf("foreign host: %d", r.StatusCode)
	}
	// Cross-origin POST.
	req, _ = http.NewRequest("POST", page+"login", strings.NewReader("url=x"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "https://evil.example")
	if r, _ := noRedirect.Do(req); r.StatusCode != 403 {
		t.Fatalf("cross-origin post: %d", r.StatusCode)
	}

	post := func(path string, form url.Values) *http.Response {
		t.Helper()
		req, _ := http.NewRequest("POST", page+path, strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Origin", u.Scheme+"://"+u.Host)
		r, err := noRedirect.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	r := post("login", url.Values{"url": {"https://p/x"}, "token": {"tk"}})
	if r.StatusCode != 303 || !strings.Contains(r.Header.Get("Location"), "m=") {
		t.Fatalf("login: %d %s", r.StatusCode, r.Header.Get("Location"))
	}
	r = post("login", url.Values{"url": {"bad"}})
	loc := r.Header.Get("Location")
	if !strings.Contains(loc, "e=invalid") {
		t.Fatalf("login error redirect: %s", loc)
	}
	// Error message is HTML-escaped on render.
	resp, _ = noRedirect.Get(u.Scheme + "://" + u.Host + loc)
	body, _ = io.ReadAll(resp.Body)
	if strings.Contains(string(body), "<url>") || !strings.Contains(string(body), "&lt;url&gt;") {
		t.Fatal("error not escaped")
	}
	if r := post("settings", url.Values{"port": {"17890"}, "refresh": {"60"}}); r.StatusCode != 303 || ctl.port != 17890 || ctl.refresh != 60 {
		t.Fatalf("settings: %d port=%d refresh=%d", r.StatusCode, ctl.port, ctl.refresh)
	}
	if r := post("settings", url.Values{"port": {"abc"}}); !strings.Contains(r.Header.Get("Location"), "e=") {
		t.Fatal("bad port accepted")
	}
	if len(ctl.logins) != 2 || ctl.logins[0] != [2]string{"https://p/x", "tk"} {
		t.Fatalf("logins = %v", ctl.logins)
	}
}
