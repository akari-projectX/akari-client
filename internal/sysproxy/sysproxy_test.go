package sysproxy

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

var ep = Endpoint{Host: "127.0.0.1", Port: 7890}

func TestWindowsSettings(t *testing.T) {
	s := WindowsSettings(ep)
	if s.ProxyEnable != 1 || s.ProxyServer != "127.0.0.1:7890" {
		t.Fatalf("%+v", s)
	}
	for _, must := range []string{"localhost", "127.*", "10.*", "192.168.*", "172.31.*", "<local>"} {
		if !strings.Contains(";"+s.ProxyOverride+";", ";"+must+";") {
			t.Errorf("override missing %q", must)
		}
	}
	if strings.Contains(s.ProxyOverride, "/") {
		t.Error("WinINet override must not contain CIDRs")
	}
}

func TestWindowsPointsAt(t *testing.T) {
	for in, want := range map[string]bool{
		"127.0.0.1:7890": true,
		"http=127.0.0.1:7890;https=127.0.0.1:7890": true,
		"127.0.0.1:7891":  false,
		"corp-proxy:8080": false,
		"":                false,
		"http=corp:80;https=corp:80;socks=127.0.0.1:1": false,
	} {
		if got := WindowsPointsAt(in, ep); got != want {
			t.Errorf("WindowsPointsAt(%q) = %v", in, got)
		}
	}
}

func TestDarwin(t *testing.T) {
	out := "An asterisk (*) denotes that a network service is disabled.\nWi-Fi\n*Thunderbolt Bridge\nUSB 10/100/1000 LAN\n\n"
	svcs := ParseDarwinServices(out)
	if !reflect.DeepEqual(svcs, []string{"Wi-Fi", "USB 10/100/1000 LAN"}) {
		t.Fatalf("services = %q", svcs)
	}
	cmds := DarwinEnableCommands(svcs[:1], ep)
	want := [][]string{
		{networksetup, "-setwebproxy", "Wi-Fi", "127.0.0.1", "7890"},
		{networksetup, "-setsecurewebproxy", "Wi-Fi", "127.0.0.1", "7890"},
		{networksetup, "-setsocksfirewallproxy", "Wi-Fi", "127.0.0.1", "7890"},
		append([]string{networksetup, "-setproxybypassdomains", "Wi-Fi"}, DefaultBypass...),
		{networksetup, "-setwebproxystate", "Wi-Fi", "on"},
		{networksetup, "-setsecurewebproxystate", "Wi-Fi", "on"},
		{networksetup, "-setsocksfirewallproxystate", "Wi-Fi", "on"},
	}
	if !reflect.DeepEqual(cmds, want) {
		t.Fatalf("enable cmds:\n%q\nwant\n%q", cmds, want)
	}
	if got := DarwinDisableCommands(svcs); len(got) != 6 || got[3][2] != "USB 10/100/1000 LAN" || got[5][3] != "off" {
		t.Fatalf("disable cmds: %q", got)
	}
	on := "Enabled: Yes\nServer: 127.0.0.1\nPort: 7890\nAuthenticated Proxy Enabled: 0\n"
	if !DarwinPointsAt(on, ep) || DarwinPointsAt(strings.Replace(on, "Yes", "No", 1), ep) || DarwinPointsAt(strings.Replace(on, "7890", "8080", 1), ep) {
		t.Fatal("DarwinPointsAt")
	}
}

func lookPath(have ...string) func(string) (string, error) {
	return func(name string) (string, error) {
		for _, h := range have {
			if h == name {
				return "/usr/bin/" + name, nil
			}
		}
		return "", errors.New("not found")
	}
}

func TestDetectDesktop(t *testing.T) {
	cases := []struct {
		xdg   string
		tools []string
		want  Desktop
	}{
		{"GNOME", []string{"gsettings"}, Desktop{GNOME: true}},
		{"ubuntu:GNOME", []string{"gsettings", "kwriteconfig5"}, Desktop{GNOME: true}},
		{"KDE", []string{"gsettings", "kwriteconfig6", "kwriteconfig5"}, Desktop{KDE: true, KWrite: "kwriteconfig6"}},
		{"KDE", []string{"kwriteconfig5"}, Desktop{KDE: true, KWrite: "kwriteconfig5"}},
		{"", []string{"gsettings", "kwriteconfig5"}, Desktop{GNOME: true, KDE: true, KWrite: "kwriteconfig5"}},
		{"XFCE", nil, Desktop{}},
	}
	for _, c := range cases {
		if got := DetectDesktop(c.xdg, lookPath(c.tools...)); got != c.want {
			t.Errorf("DetectDesktop(%q, %v) = %+v, want %+v", c.xdg, c.tools, got, c.want)
		}
	}
}

func TestLinuxCommands(t *testing.T) {
	g := LinuxEnableCommands(Desktop{GNOME: true}, ep)
	if len(g) != 8 {
		t.Fatalf("gnome cmds: %q", g)
	}
	if !reflect.DeepEqual(g[0], []string{"gsettings", "set", "org.gnome.system.proxy.http", "host", "127.0.0.1"}) ||
		!reflect.DeepEqual(g[5], []string{"gsettings", "set", "org.gnome.system.proxy.socks", "port", "7890"}) ||
		!reflect.DeepEqual(g[7], []string{"gsettings", "set", "org.gnome.system.proxy", "mode", "manual"}) {
		t.Fatalf("gnome cmds: %q", g)
	}
	if !strings.HasPrefix(g[6][4], "['localhost', '127.0.0.1'") {
		t.Fatalf("ignore-hosts: %q", g[6][4])
	}
	k := LinuxEnableCommands(Desktop{KDE: true, KWrite: "kwriteconfig6"}, ep)
	if k[0][0] != "kwriteconfig6" || k[0][len(k[0])-1] != "http://127.0.0.1 7890" || k[2][len(k[2])-1] != "socks://127.0.0.1 7890" || k[len(k)-1][0] != "dbus-send" {
		t.Fatalf("kde cmds: %q", k)
	}
	if got := LinuxDisableCommands(Desktop{GNOME: true}); !reflect.DeepEqual(got, [][]string{{"gsettings", "set", "org.gnome.system.proxy", "mode", "none"}}) {
		t.Fatalf("disable: %q", got)
	}
	if len(LinuxEnableCommands(Desktop{}, ep)) != 0 {
		t.Fatal("no desktop must yield no commands")
	}
}
