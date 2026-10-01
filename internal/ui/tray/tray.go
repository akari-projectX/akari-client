// Package tray is the system-tray UI (fyne.io/systray, Apache-2.0).
package tray

import (
	"context"
	"fmt"
	"log/slog"
	"runtime"
	"sync"
	"time"

	"fyne.io/systray"

	"github.com/akari-projectX/akari-client/internal/app"
	"github.com/akari-projectX/akari-client/internal/core"
	"github.com/akari-projectX/akari-client/internal/supervisor"
	"github.com/akari-projectX/akari-client/internal/ui/icon"
	"github.com/akari-projectX/akari-client/internal/ui/open"
	"github.com/akari-projectX/akari-client/internal/version"
)

// maxNodes is the number of pre-allocated node menu entries (systray menus
// cannot be rebuilt reliably on every platform; surplus items are hidden).
const maxNodes = 128

// Deps are the tray's collaborators.
type Deps struct {
	App      *app.App
	Settings interface{ URL() (string, error) }
	LogDir   string
	Log      *slog.Logger
	// OpenSettingsOnStart opens the settings page when not signed in.
	OpenSettingsOnStart bool
}

// Run blocks on the platform UI loop until Quit. onQuit runs before the
// loop exits (stop the app there).
func Run(d Deps, onQuit func()) {
	systray.Run(func() { onReady(d) }, onQuit)
}

type ui struct {
	mu       sync.Mutex // serializes refresh and guards nodeName/lastIcon
	d        Deps
	status   *systray.MenuItem
	usage    *systray.MenuItem
	connect  *systray.MenuItem
	sysproxy *systray.MenuItem
	nodes    *systray.MenuItem
	test     *systray.MenuItem
	nodeItem [maxNodes]*systray.MenuItem
	nodeName [maxNodes]string
	icons    map[string][]byte
	lastIcon string
}

func onReady(d Deps) {
	u := &ui{d: d, icons: map[string][]byte{
		"on":   icon.ForOS(runtime.GOOS, icon.Connected),
		"off":  icon.ForOS(runtime.GOOS, icon.Disconnected),
		"warn": icon.ForOS(runtime.GOOS, icon.Warning),
	}}
	systray.SetIcon(u.icons["off"])
	systray.SetTooltip("Akari")

	u.status = systray.AddMenuItem("Akari", "")
	u.status.Disable()
	u.usage = systray.AddMenuItem("", "")
	u.usage.Disable()
	u.usage.Hide()
	systray.AddSeparator()
	u.connect = systray.AddMenuItem("Connect", "Start or stop the proxy")
	u.sysproxy = systray.AddMenuItemCheckbox("System proxy", "Route system traffic through Akari", false)
	u.nodes = systray.AddMenuItem("Nodes", "Choose a node")
	u.test = u.nodes.AddSubMenuItem("Test latency", "Measure every node")
	for i := range u.nodeItem {
		it := u.nodes.AddSubMenuItemCheckbox("", "", false)
		it.Hide()
		u.nodeItem[i] = it
		go u.onNode(i, it)
	}
	systray.AddSeparator()
	refresh := systray.AddMenuItem("Refresh subscription", "Fetch the latest node list")
	settingsItem := systray.AddMenuItem("Settings…", "Sign in and preferences")
	logs := systray.AddMenuItem("Open logs", "Show the log folder")
	systray.AddSeparator()
	about := systray.AddMenuItem("akari-client "+version.Version+" (mihomo "+core.MihomoVersion+")", "")
	about.Disable()
	quit := systray.AddMenuItem("Quit", "Quit Akari")

	ctx := context.Background()
	go func() {
		for range u.connect.ClickedCh {
			st := d.App.Status()
			var err error
			if st.Core == supervisor.Stopped {
				err = d.App.Connect(ctx)
				if err != nil && !st.Configured {
					u.openSettings()
				}
			} else {
				err = d.App.Disconnect(ctx)
			}
			if err != nil {
				d.Log.Warn("connect toggle", "err", err)
			}
			u.refresh()
		}
	}()
	go func() {
		for range u.sysproxy.ClickedCh {
			if err := d.App.SetSystemProxy(ctx, !u.sysproxy.Checked()); err != nil {
				d.Log.Warn("system proxy toggle", "err", err)
			}
			u.refresh()
		}
	}()
	go func() {
		for range u.test.ClickedCh {
			u.test.SetTitle("Testing…")
			u.test.Disable()
			tctx, cancel := context.WithTimeout(ctx, 30*time.Second)
			d.App.TestDelays(tctx)
			cancel()
			u.test.SetTitle("Test latency")
			u.test.Enable()
			u.refresh()
		}
	}()
	go func() {
		for range refresh.ClickedCh {
			d.App.RefreshNow()
		}
	}()
	go func() {
		for range settingsItem.ClickedCh {
			u.openSettings()
		}
	}()
	go func() {
		for range logs.ClickedCh {
			if err := open.Open(d.LogDir); err != nil {
				d.Log.Warn("open logs", "err", err)
			}
		}
	}()
	go func() {
		<-quit.ClickedCh
		systray.Quit()
	}()
	go func() {
		tick := time.NewTicker(30 * time.Second)
		defer tick.Stop()
		for {
			select {
			case <-d.App.Changes():
			case <-tick.C:
			}
			u.refresh()
		}
	}()
	u.refresh()
	if d.OpenSettingsOnStart && !d.App.Status().Configured {
		u.openSettings()
	}
}

func (u *ui) openSettings() {
	url, err := u.d.Settings.URL()
	if err == nil {
		err = open.Open(url)
	}
	if err != nil {
		u.d.Log.Warn("open settings", "err", err)
	}
}

func (u *ui) onNode(i int, it *systray.MenuItem) {
	for range it.ClickedCh {
		u.mu.Lock()
		name := u.nodeName[i]
		u.mu.Unlock()
		if name == "" {
			continue
		}
		if err := u.d.App.SelectNode(name); err != nil {
			u.d.Log.Warn("select node", "node", name, "err", err)
		}
		u.refresh()
	}
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

// NodeLabel formats a node entry ("hk-1    42 ms").
func NodeLabel(n core.Node) string {
	switch {
	case n.DelayMS > 0:
		return fmt.Sprintf("%s    %d ms", n.Name, n.DelayMS)
	case n.DelayMS < 0:
		return n.Name + "    timeout"
	default:
		return n.Name
	}
}

func (u *ui) refresh() {
	st := u.d.App.Status()
	u.mu.Lock()
	defer u.mu.Unlock()
	var line, ic string
	switch {
	case !st.Configured:
		line, ic = "Not signed in — open Settings…", "off"
	case st.Core == supervisor.Running:
		line, ic = "Connected", "on"
		if st.Selected != "" {
			line += " — " + st.Selected
		}
		if st.SubErr != "" || st.SysProxyErr != "" {
			ic = "warn"
		}
	case st.Core == supervisor.Backoff:
		line, ic = "Reconnecting… ("+trim(st.CoreErr, 60)+")", "warn"
	default:
		line, ic = "Disconnected", "off"
	}
	u.status.SetTitle(line)
	systray.SetTooltip("Akari — " + line)
	if ic != u.lastIcon {
		systray.SetIcon(u.icons[ic])
		u.lastIcon = ic
	}
	if inf := st.UserInfo; inf != nil {
		total := "∞"
		if inf.Total > 0 {
			total = human(inf.Total)
		}
		txt := "Used " + human(inf.Upload+inf.Download) + " / " + total
		if inf.Expire > 0 {
			txt += " · expires " + time.Unix(inf.Expire, 0).Format("2006-01-02")
		}
		u.usage.SetTitle(txt)
		u.usage.Show()
	} else {
		u.usage.Hide()
	}

	if st.Core == supervisor.Stopped {
		u.connect.SetTitle("Connect")
	} else {
		u.connect.SetTitle("Disconnect")
	}
	if st.SystemProxy || u.d.App.Settings().SystemProxy {
		u.sysproxy.Check()
	} else {
		u.sysproxy.Uncheck()
	}
	if len(st.Nodes) == 0 {
		u.nodes.Disable()
	} else {
		u.nodes.Enable()
	}
	for i, it := range u.nodeItem {
		if i >= len(st.Nodes) {
			u.nodeName[i] = ""
			it.Hide()
			continue
		}
		n := st.Nodes[i]
		u.nodeName[i] = n.Name
		it.SetTitle(NodeLabel(n))
		if n.Name == st.Selected {
			it.Check()
		} else {
			it.Uncheck()
		}
		it.Show()
	}
}

func trim(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

// Quit ends the tray loop (safe from any goroutine).
func Quit() { systray.Quit() }
