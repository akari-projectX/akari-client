// Package web serves the small local settings page (login + preferences).
//
// It listens on 127.0.0.1 on a random port and every route lives under a
// random 256-bit path secret, so other local users/processes and web pages
// cannot drive it: requests must carry the secret path, an exact
// Host header (DNS-rebinding defence) and, for POSTs, a same-origin
// Origin header when the browser sends one.
package web

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"html/template"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/akari-projectX/akari-client/internal/app"
	"github.com/akari-projectX/akari-client/internal/settings"
	"github.com/akari-projectX/akari-client/internal/version"
)

// Controller is the part of app.App the page drives.
type Controller interface {
	Status() app.Status
	Settings() settings.Settings
	Login(ctx context.Context, input, token string) error
	Logout(ctx context.Context) error
	SetPort(ctx context.Context, port int) error
	SetRefreshMinutes(m int) error
	RefreshNow()
}

// Server is started lazily by URL().
type Server struct {
	c   Controller
	log *slog.Logger

	mu     sync.Mutex
	srv    *http.Server
	base   string // http://127.0.0.1:port
	secret string
}

// New returns an unstarted server.
func New(c Controller, log *slog.Logger) *Server { return &Server{c: c, log: log} }

// URL starts the server if needed and returns the page URL.
func (s *Server) URL() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.srv == nil {
		if err := s.startLocked(); err != nil {
			return "", err
		}
	}
	return s.base + "/" + s.secret + "/", nil
}

func (s *Server) startLocked() error {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return err
	}
	s.secret = base64.RawURLEncoding.EncodeToString(b[:])
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("settings page listen: %w", err)
	}
	s.base = "http://" + l.Addr().String()
	s.srv = &http.Server{
		Handler:           s.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      90 * time.Second,
		MaxHeaderBytes:    16 << 10,
	}
	go func() {
		if err := s.srv.Serve(l); err != nil && !errors.Is(err, http.ErrServerClosed) {
			s.log.Error("settings page", "err", err)
		}
	}()
	return nil
}

// Close stops the server.
func (s *Server) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.srv == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err := s.srv.Shutdown(ctx)
	s.srv = nil
	return err
}

// Handler is exported for tests (base and secret must be set).
func (s *Server) Handler() http.Handler {
	return http.HandlerFunc(s.serve)
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; form-action 'self'; frame-ancestors 'none'; base-uri 'none'")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Cache-Control", "no-store")

	s.mu.Lock()
	base, secret := s.base, s.secret
	s.mu.Unlock()
	if secret == "" || "http://"+r.Host != base {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	rest, ok := strings.CutPrefix(r.URL.Path, "/"+secret+"/")
	if !ok {
		http.NotFound(w, r)
		return
	}
	if r.Method == http.MethodPost {
		if o := r.Header.Get("Origin"); o != "" && o != base {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
	}
	page := "/" + secret + "/"
	redirect := func(msg string, err error) {
		q := url.Values{}
		if err != nil {
			q.Set("e", err.Error())
		} else if msg != "" {
			q.Set("m", msg)
		}
		http.Redirect(w, r, page+"?"+q.Encode(), http.StatusSeeOther)
	}
	switch {
	case rest == "" && r.Method == http.MethodGet:
		s.render(w, r)
	case rest == "login" && r.Method == http.MethodPost:
		ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
		defer cancel()
		err := s.c.Login(ctx, r.PostFormValue("url"), r.PostFormValue("token"))
		redirect("Signed in and connected.", err)
	case rest == "settings" && r.Method == http.MethodPost:
		port, err := strconv.Atoi(strings.TrimSpace(r.PostFormValue("port")))
		if err != nil {
			redirect("", errors.New("port must be a number"))
			return
		}
		mins := 0
		if v := strings.TrimSpace(r.PostFormValue("refresh")); v != "" {
			if mins, err = strconv.Atoi(v); err != nil || mins < 0 {
				redirect("", errors.New("refresh interval must be a non-negative number of minutes"))
				return
			}
		}
		if err := s.c.SetRefreshMinutes(mins); err != nil {
			redirect("", err)
			return
		}
		redirect("Settings saved.", s.c.SetPort(r.Context(), port))
	case rest == "refresh" && r.Method == http.MethodPost:
		s.c.RefreshNow()
		redirect("Refresh requested.", nil)
	case rest == "logout" && r.Method == http.MethodPost:
		redirect("Signed out.", s.c.Logout(r.Context()))
	default:
		http.NotFound(w, r)
	}
}

type view struct {
	Version string
	Msg     string
	Err     string
	St      app.Status
	Set     settings.Settings
	Usage   string
	Expire  string
}

func human(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(b)/float64(div), "KMGTPE"[exp])
}

func (s *Server) render(w http.ResponseWriter, r *http.Request) {
	st := s.c.Status()
	v := view{Version: version.Version, Msg: r.URL.Query().Get("m"), Err: r.URL.Query().Get("e"), St: st, Set: s.c.Settings()}
	if u := st.UserInfo; u != nil {
		total := "unlimited"
		if u.Total > 0 {
			total = human(u.Total)
		}
		v.Usage = human(u.Upload+u.Download) + " / " + total
		if u.Expire > 0 {
			v.Expire = time.Unix(u.Expire, 0).Format("2006-01-02")
		}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := pageTmpl.Execute(w, v); err != nil {
		s.log.Error("render settings page", "err", err)
	}
}

var pageTmpl = template.Must(template.New("page").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>Akari settings</title>
<style>
:root{color-scheme:light dark;--bg:#fff;--fg:#1b1b1f;--mut:#6b6b76;--bd:#d9d9e0;--acc:#5b5bd6;--err:#c62828;--ok:#2e7d32}
@media (prefers-color-scheme:dark){:root{--bg:#18181b;--fg:#ececf1;--mut:#a1a1aa;--bd:#33333a;--acc:#8b8bf5;--err:#ef9a9a;--ok:#a5d6a7}}
body{background:var(--bg);color:var(--fg);font:15px/1.5 system-ui,sans-serif;max-width:560px;margin:0 auto;padding:24px 16px}
h1{font-size:20px;margin:0 0 4px}h2{font-size:15px;margin:24px 0 8px}
.mut{color:var(--mut);font-size:13px}.err{color:var(--err)}.ok{color:var(--ok)}
label{display:block;margin:10px 0 4px;font-size:13px;color:var(--mut)}
input{width:100%;box-sizing:border-box;padding:8px;border:1px solid var(--bd);border-radius:6px;background:transparent;color:inherit;font:inherit}
button{margin-top:12px;padding:8px 14px;border:0;border-radius:6px;background:var(--acc);color:#fff;font:inherit;cursor:pointer}
button.sec{background:transparent;color:var(--fg);border:1px solid var(--bd)}
dl{display:grid;grid-template-columns:auto 1fr;gap:4px 16px;margin:0}dt{color:var(--mut)}dd{margin:0}
form.inline{display:inline}
</style></head><body>
<h1>Akari</h1><div class="mut">akari-client {{.Version}}</div>
{{if .Msg}}<p class="ok">{{.Msg}}</p>{{end}}
{{if .Err}}<p class="err">{{.Err}}</p>{{end}}
{{if .St.Configured}}
<h2>Status</h2>
<dl>
<dt>Panel</dt><dd>{{.St.PanelHost}}</dd>
<dt>Core</dt><dd>{{.St.Core}}{{if .St.CoreErr}} <span class="err">({{.St.CoreErr}})</span>{{end}}</dd>
<dt>Local proxy</dt><dd>127.0.0.1:{{.St.Port}}</dd>
<dt>System proxy</dt><dd>{{if .St.SystemProxy}}on{{else}}off{{end}}{{if .St.SysProxyErr}} <span class="err">({{.St.SysProxyErr}})</span>{{end}}</dd>
{{if .St.Selected}}<dt>Node</dt><dd>{{.St.Selected}}</dd>{{end}}
{{if .Usage}}<dt>Traffic</dt><dd>{{.Usage}}</dd>{{end}}
{{if .Expire}}<dt>Expires</dt><dd>{{.Expire}}</dd>{{end}}
<dt>Updated</dt><dd>{{if .St.LastFetch.IsZero}}never{{else}}{{.St.LastFetch.Local.Format "2006-01-02 15:04"}}{{end}}{{if .St.SubErr}} <span class="err">({{.St.SubErr}})</span>{{end}}</dd>
</dl>
<form class="inline" method="post" action="refresh"><button class="sec">Refresh subscription</button></form>
<form class="inline" method="post" action="logout"><button class="sec">Sign out</button></form>
{{end}}
<h2>{{if .St.Configured}}Change subscription{{else}}Sign in{{end}}</h2>
<form method="post" action="login" autocomplete="off">
<label for="url">Subscription URL (or panel URL)</label>
<input id="url" name="url" type="url" required placeholder="https://panel.example.com/&lt;prefix&gt;/sub/&lt;token&gt;">
<label for="token">Token (only if you entered the panel URL)</label>
<input id="token" name="token" type="password" placeholder="optional">
<button>Sign in</button>
</form>
<h2>Preferences</h2>
<form method="post" action="settings">
<label for="port">Local mixed port (HTTP + SOCKS5, 127.0.0.1 only)</label>
<input id="port" name="port" type="number" min="1024" max="65535" value="{{.St.Port}}">
<label for="refresh">Refresh interval in minutes (empty or 0 = panel default; min 15)</label>
<input id="refresh" name="refresh" type="number" min="0" value="{{if .Set.RefreshMinutes}}{{.Set.RefreshMinutes}}{{end}}">
<button>Save</button>
</form>
</body></html>
`))
