// Package sysproxy points the operating system's proxy settings at the
// local mixed port and restores them.
//
// The OS-specific plans (registry values, networksetup / gsettings /
// kwriteconfig command lines) are produced by pure functions in this file
// so they can be unit-tested on any platform; the *_<os>.go files only
// execute them.
package sysproxy

import (
	"context"
	"fmt"
	"net"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// Endpoint is the local proxy the OS should use.
type Endpoint struct {
	Host string // always 127.0.0.1 for this client
	Port int
}

func (e Endpoint) String() string { return net.JoinHostPort(e.Host, strconv.Itoa(e.Port)) }

// DefaultBypass are destinations that never go through the proxy.
var DefaultBypass = []string{
	"localhost", "127.0.0.1", "::1", "*.local",
	"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "169.254.0.0/16",
}

// Manager applies and reverts the system proxy.
type Manager interface {
	// Enable points the system proxy at ep.
	Enable(ctx context.Context, ep Endpoint) error
	// Disable turns the system proxy off, but only if it still points at
	// ep (where the platform lets us check), so settings the user changed
	// meanwhile are left alone.
	Disable(ctx context.Context, ep Endpoint) error
}

// Runner executes external commands (faked in tests).
type Runner interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
}

// ExecRunner runs real commands with a timeout.
type ExecRunner struct{}

func (ExecRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	if err != nil {
		return out, fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return out, nil
}

// ---------------------------------------------------------------- Windows

// WinINetSettings are the values written under
// HKCU\Software\Microsoft\Windows\CurrentVersion\Internet Settings.
type WinINetSettings struct {
	ProxyEnable   uint32
	ProxyServer   string
	ProxyOverride string
}

// WindowsSettings returns the WinINet values for ep. WinINet bypass
// entries use wildcards, not CIDRs; "<local>" bypasses plain host names.
func WindowsSettings(ep Endpoint) WinINetSettings {
	return WinINetSettings{
		ProxyEnable: 1,
		ProxyServer: ep.String(),
		ProxyOverride: strings.Join([]string{
			"localhost", "127.*", "10.*",
			"172.16.*", "172.17.*", "172.18.*", "172.19.*", "172.20.*", "172.21.*", "172.22.*", "172.23.*",
			"172.24.*", "172.25.*", "172.26.*", "172.27.*", "172.28.*", "172.29.*", "172.30.*", "172.31.*",
			"192.168.*", "169.254.*", "*.local", "<local>",
		}, ";"),
	}
}

// WindowsPointsAt reports whether a ProxyServer value targets ep (either
// "host:port" or the per-protocol form "http=host:port;https=...").
func WindowsPointsAt(proxyServer string, ep Endpoint) bool {
	want := ep.String()
	for _, part := range strings.Split(proxyServer, ";") {
		part = strings.TrimSpace(part)
		if _, v, ok := strings.Cut(part, "="); ok {
			part = v
		}
		if strings.EqualFold(part, want) {
			return true
		}
	}
	return false
}

// ------------------------------------------------------------------ macOS

const networksetup = "/usr/sbin/networksetup"

// ParseDarwinServices parses `networksetup -listallnetworkservices`:
// the first line is an explanatory banner; disabled services start with '*'.
func ParseDarwinServices(out string) []string {
	var svcs []string
	for i, line := range strings.Split(strings.ReplaceAll(out, "\r\n", "\n"), "\n") {
		line = strings.TrimSpace(line)
		if i == 0 && strings.Contains(line, "asterisk") {
			continue
		}
		if line == "" || strings.HasPrefix(line, "*") {
			continue
		}
		svcs = append(svcs, line)
	}
	return svcs
}

// DarwinEnableCommands returns networksetup invocations for each service.
func DarwinEnableCommands(services []string, ep Endpoint) [][]string {
	port := strconv.Itoa(ep.Port)
	var cmds [][]string
	for _, s := range services {
		cmds = append(cmds,
			[]string{networksetup, "-setwebproxy", s, ep.Host, port},
			[]string{networksetup, "-setsecurewebproxy", s, ep.Host, port},
			[]string{networksetup, "-setsocksfirewallproxy", s, ep.Host, port},
			append([]string{networksetup, "-setproxybypassdomains", s}, DefaultBypass...),
			[]string{networksetup, "-setwebproxystate", s, "on"},
			[]string{networksetup, "-setsecurewebproxystate", s, "on"},
			[]string{networksetup, "-setsocksfirewallproxystate", s, "on"},
		)
	}
	return cmds
}

// DarwinDisableCommands turns the three proxies off for each service.
func DarwinDisableCommands(services []string) [][]string {
	var cmds [][]string
	for _, s := range services {
		cmds = append(cmds,
			[]string{networksetup, "-setwebproxystate", s, "off"},
			[]string{networksetup, "-setsecurewebproxystate", s, "off"},
			[]string{networksetup, "-setsocksfirewallproxystate", s, "off"},
		)
	}
	return cmds
}

// DarwinPointsAt parses `networksetup -getwebproxy <svc>` output.
func DarwinPointsAt(out string, ep Endpoint) bool {
	var enabled, server, port string
	for _, line := range strings.Split(out, "\n") {
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		switch strings.TrimSpace(k) {
		case "Enabled":
			enabled = strings.TrimSpace(v)
		case "Server":
			server = strings.TrimSpace(v)
		case "Port":
			port = strings.TrimSpace(v)
		}
	}
	return enabled == "Yes" && server == ep.Host && port == strconv.Itoa(ep.Port)
}

// ------------------------------------------------------------------ Linux

// Desktop identifies which Linux settings backends to drive.
type Desktop struct {
	GNOME bool // gsettings (GNOME, Cinnamon, Budgie, Unity, ...)
	KDE   bool // kwriteconfig5/6 (Plasma)
	// KWrite is the kwriteconfig binary name found on PATH.
	KWrite string
}

// DetectDesktop chooses backends from $XDG_CURRENT_DESKTOP and the tools
// on PATH (lookPath = exec.LookPath in production).
func DetectDesktop(xdgCurrentDesktop string, lookPath func(string) (string, error)) Desktop {
	var d Desktop
	cur := strings.ToUpper(xdgCurrentDesktop)
	if _, err := lookPath("gsettings"); err == nil && !strings.Contains(cur, "KDE") {
		d.GNOME = true
	}
	if strings.Contains(cur, "KDE") || cur == "" {
		for _, k := range []string{"kwriteconfig6", "kwriteconfig5"} {
			if _, err := lookPath(k); err == nil {
				d.KDE, d.KWrite = true, k
				break
			}
		}
	}
	return d
}

func gsettingsList(items []string) string {
	q := make([]string, len(items))
	for i, s := range items {
		q[i] = "'" + strings.ReplaceAll(s, "'", "") + "'"
	}
	return "[" + strings.Join(q, ", ") + "]"
}

// LinuxEnableCommands returns the command lines for the detected desktop.
func LinuxEnableCommands(d Desktop, ep Endpoint) [][]string {
	port := strconv.Itoa(ep.Port)
	var cmds [][]string
	if d.GNOME {
		const base = "org.gnome.system.proxy"
		for _, scheme := range []string{"http", "https", "socks"} {
			cmds = append(cmds,
				[]string{"gsettings", "set", base + "." + scheme, "host", ep.Host},
				[]string{"gsettings", "set", base + "." + scheme, "port", port},
			)
		}
		cmds = append(cmds,
			[]string{"gsettings", "set", base, "ignore-hosts", gsettingsList(DefaultBypass)},
			[]string{"gsettings", "set", base, "mode", "manual"},
		)
	}
	if d.KDE {
		kw := func(key, val string) []string {
			return []string{d.KWrite, "--file", "kioslaverc", "--group", "Proxy Settings", "--key", key, val}
		}
		cmds = append(cmds,
			kw("httpProxy", "http://"+ep.Host+" "+port),
			kw("httpsProxy", "http://"+ep.Host+" "+port),
			kw("socksProxy", "socks://"+ep.Host+" "+port),
			kw("NoProxyFor", strings.Join(DefaultBypass, ",")),
			kw("ProxyType", "1"),
			kdeReparse(),
		)
	}
	return cmds
}

// LinuxDisableCommands switches the proxy mode off.
func LinuxDisableCommands(d Desktop) [][]string {
	var cmds [][]string
	if d.GNOME {
		cmds = append(cmds, []string{"gsettings", "set", "org.gnome.system.proxy", "mode", "none"})
	}
	if d.KDE {
		cmds = append(cmds,
			[]string{d.KWrite, "--file", "kioslaverc", "--group", "Proxy Settings", "--key", "ProxyType", "0"},
			kdeReparse(),
		)
	}
	return cmds
}

func kdeReparse() []string {
	return []string{"dbus-send", "--type=signal", "/KIO/Scheduler", "org.kde.KIO.Scheduler.reparseSlaveConfiguration", "string:"}
}

// GnomePointsAt reports whether gsettings values (as printed by
// `gsettings get`) describe ep in manual mode.
func GnomePointsAt(mode, host, port string, ep Endpoint) bool {
	unq := func(s string) string { return strings.Trim(strings.TrimSpace(s), "'") }
	return unq(mode) == "manual" && unq(host) == ep.Host && strings.TrimSpace(port) == strconv.Itoa(ep.Port)
}

// runAll executes cmds, continuing past failures; returns the first error.
func runAll(ctx context.Context, r Runner, cmds [][]string) error {
	var first error
	for _, c := range cmds {
		if _, err := r.Run(ctx, c[0], c[1:]...); err != nil && first == nil {
			first = err
		}
	}
	return first
}
