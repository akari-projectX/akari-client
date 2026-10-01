package core

import (
	"fmt"
	"net"
	"strings"
	"testing"
)

const directProfile = `
proxies:
  - name: "node-a"
    type: direct
  - name: "node-b"
    type: direct
proxy-groups:
  - name: PROXY
    type: select
    proxies:
      - "node-a"
      - "node-b"
rules:
  - MATCH,PROXY
`

// The panel's render_clash output shape (vless + reality), padded with
// trailing newlines exactly like sub.rs pad().
const panelProfile = `proxies:
  - name: "hk-1"
    type: vless
    server: 203.0.113.10
    port: 443
    uuid: 2b7c1c43-6d2e-4c4f-9a59-5b8f6f3b8a11
    flow: xtls-rprx-vision
    network: tcp
    tls: true
    servername: www.example.com
    client-fingerprint: chrome
    reality-opts:
      public-key: Z84J2IelR9ch3k8VtlVhhs5ycBUlXA7wHBWcBrjqnAw
      short-id: 6ba85179e30d4fc2
  - name: "jp-1"
    type: trojan
    server: jp.example.com
    port: 443
    password: secret
    network: ws
    tls: true
    servername: jp.example.com
    ws-opts:
      path: /ws
      headers:
        Host: jp.example.com
proxy-groups:
  - name: PROXY
    type: select
    proxies:
      - "hk-1"
      - "jp-1"
rules:
  - MATCH,PROXY
`

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func TestBuildConfigPanelShape(t *testing.T) {
	p, err := BuildConfig([]byte(panelProfile+strings.Repeat("\n", 5000)), Options{MixedPort: 7890})
	if err != nil {
		t.Fatal(err)
	}
	if p.Group != "PROXY" {
		t.Fatalf("group = %q", p.Group)
	}
	if fmt.Sprint(p.Nodes) != "[hk-1 jp-1]" {
		t.Fatalf("nodes = %v", p.Nodes)
	}
}

func TestBuildConfigDropsDangerousKeys(t *testing.T) {
	hostile := `
mixed-port: 1080
port: 8080
socks-port: 1081
allow-lan: true
bind-address: "*"
external-controller: 0.0.0.0:9090
secret: ""
authentication: ["a:b"]
tun:
  enable: true
listeners:
  - name: lan
    type: mixed
    port: 9999
    listen: 0.0.0.0
proxy-providers:
  evil:
    type: http
    url: http://evil.example/p.yaml
    interval: 10
` + directProfile
	p, err := BuildConfig([]byte(hostile), Options{MixedPort: 17890})
	if err != nil {
		t.Fatal(err)
	}
	g := p.cfg.General
	if g.AllowLan || g.MixedPort != 17890 || g.Port != 0 || g.SocksPort != 0 || g.Tun.Enable {
		t.Fatalf("general not sanitized: allowLan=%v mixed=%d port=%d socks=%d tun=%v", g.AllowLan, g.MixedPort, g.Port, g.SocksPort, g.Tun.Enable)
	}
	if p.cfg.Controller.ExternalController != "" {
		t.Fatalf("external controller leaked: %q", p.cfg.Controller.ExternalController)
	}
	if _, ok := p.cfg.Providers["evil"]; ok || len(p.cfg.Listeners) != 0 || len(p.cfg.Users) != 0 {
		t.Fatalf("listeners/providers/users leaked: %d %v %d", len(p.cfg.Listeners), p.cfg.Providers, len(p.cfg.Users))
	}
}

func TestBuildConfigRejects(t *testing.T) {
	for name, in := range map[string]string{
		"empty":       "",
		"not yaml":    "\x00\x01{{{",
		"no proxies":  "proxies: []\nrules: [MATCH,DIRECT]\n",
		"bad group":   directProfile + "  - name: X\n    type: select\n    proxies: [missing]\n",
		"bad rule":    strings.Replace(directProfile, "MATCH,PROXY", "MATCH,NOPE", 1),
		"html":        "<html><body>login</body></html>",
		"bad proxy":   "proxies:\n  - name: x\n    type: vless\n",
		"links (b64)": "dmxlc3M6Ly9hYmNAMS4yLjMuNDo0NDM=",
	} {
		if err := Validate([]byte(in)); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}

func TestBuildConfigAddsMatchRule(t *testing.T) {
	in := strings.Replace(directProfile, "rules:\n  - MATCH,PROXY\n", "", 1)
	p, err := BuildConfig([]byte(in), Options{MixedPort: 0})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.cfg.Rules) != 1 || p.cfg.Rules[0].Adapter() != "PROXY" {
		t.Fatalf("rules = %v", p.cfg.Rules)
	}
}
