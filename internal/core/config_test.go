package core

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

const directNoRules = `
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
`

const directProfile = directNoRules + "rules:\n  - MATCH,PROXY\n"

// The panel's render_clash output shape (vless+reality, trojan+ws), padded
// with trailing newlines exactly like sub.rs pad().
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

func parse(t *testing.T, b []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := yaml.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestBuildConfigPanelShape(t *testing.T) {
	cfg, p, err := BuildConfig([]byte(panelProfile+strings.Repeat("\n", 5000)), Options{MixedPort: 7890, Controller: "127.0.0.1:1234", Secret: "s"})
	if err != nil {
		t.Fatal(err)
	}
	if p.Group != "PROXY" || strings.Join(p.Nodes, ",") != "hk-1,jp-1" {
		t.Fatalf("profile = %+v", p)
	}
	m := parse(t, cfg)
	if m["mixed-port"] != 7890 || m["external-controller"] != "127.0.0.1:1234" || m["secret"] != "s" || m["bind-address"] != "127.0.0.1" {
		t.Fatalf("config = %v", m)
	}
	hk := m["proxies"].([]any)[0].(map[string]any)
	if hk["client-fingerprint"] != "chrome" || hk["reality-opts"].(map[string]any)["short-id"] != "6ba85179e30d4fc2" {
		t.Fatalf("proxy fields lost: %v", hk)
	}
}

func TestBuildConfigDropsDangerousKeys(t *testing.T) {
	hostile := `
mixed-port: 1080
port: 8080
socks-port: 1081
redir-port: 1082
allow-lan: true
bind-address: "*"
external-controller: 0.0.0.0:9090
external-ui: /tmp/ui
secret: ""
authentication: ["a:b"]
tun:
  enable: true
dns:
  enable: true
  listen: 0.0.0.0:53
listeners:
  - name: lan
    type: mixed
    port: 9999
    listen: 0.0.0.0
proxy-providers:
  evil:
    type: http
    url: http://evil.example/p.yaml
rule-providers:
  evil:
    type: http
    url: http://evil.example/r.yaml
` + strings.Replace(directProfile, "    type: select\n", "    type: select\n    use: [evil]\n", 1)
	cfg, _, err := BuildConfig([]byte(hostile), Options{MixedPort: 17890})
	if err != nil {
		t.Fatal(err)
	}
	m := parse(t, cfg)
	for _, k := range []string{"port", "socks-port", "redir-port", "external-ui", "authentication", "tun", "dns", "listeners", "proxy-providers", "rule-providers"} {
		if _, ok := m[k]; ok {
			t.Errorf("key %q leaked into runtime config", k)
		}
	}
	if m["allow-lan"] != false || m["bind-address"] != "127.0.0.1" || m["mixed-port"] != 17890 || m["external-controller"] != "" {
		t.Fatalf("general not sanitized: %v", m)
	}
	g := m["proxy-groups"].([]any)[0].(map[string]any)
	if _, ok := g["use"]; ok {
		t.Fatal("group provider reference kept")
	}
}

func TestBuildConfigRejects(t *testing.T) {
	for name, in := range map[string]string{
		"empty":            "",
		"not yaml":         "\x00\x01{{{",
		"no proxies":       "proxies: []\nrules: [MATCH,DIRECT]\n",
		"html":             "<html><body>login</body></html>",
		"links (base64)":   "dmxlc3M6Ly9hYmNAMS4yLjMuNDo0NDM=",
		"nameless proxy":   "proxies:\n  - type: direct\n",
		"duplicate proxy":  "proxies:\n  - {name: a, type: direct}\n  - {name: a, type: direct}\n",
		"builtin name":     "proxies:\n  - {name: DIRECT, type: direct}\n",
		"unknown member":   directNoRules + "  - name: X\n    type: select\n    proxies: [missing]\n",
		"empty group":      directNoRules + "  - name: X\n    type: select\n    use: [p]\n",
		"group = proxy":    directNoRules + "  - name: node-a\n    type: select\n    proxies: [node-b]\n",
		"members not list": directNoRules + "  - name: X\n    type: select\n    proxies: node-a\n",
	} {
		_, _, err := BuildConfig([]byte(in), Options{})
		if err == nil {
			t.Errorf("%s: expected error", name)
			continue
		}
		t.Logf("%s: %v", name, err)
	}
}

func TestBuildConfigDefaults(t *testing.T) {
	// No rules -> MATCH to the main group; no selector -> synthetic one.
	cfg, p, err := BuildConfig([]byte("proxies:\n  - {name: a, type: direct}\n  - {name: PROXY, type: direct}\n"), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if p.Group != "PROXY_" || strings.Join(p.Nodes, ",") != "a,PROXY" {
		t.Fatalf("profile = %+v", p)
	}
	m := parse(t, cfg)
	if r := m["rules"].([]any); len(r) != 1 || r[0] != "MATCH,PROXY_" {
		t.Fatalf("rules = %v", r)
	}
	// Prefers PROXY over the first selector.
	_, p, err = BuildConfig([]byte(directNoRules+"  - name: Other\n    type: select\n    proxies: [node-a]\n"), Options{})
	if err != nil || p.Group != "PROXY" {
		t.Fatalf("group = %q", p.Group)
	}
}

func TestKernelError(t *testing.T) {
	out := `time="2026-10-02T02:23:03Z" level=info msg="Start initial configuration in progress"
time="2026-10-02T02:23:03Z" level=error msg="proxy 0: missing type \"x\""
`
	if got := kernelError(out, nil); got != `proxy 0: missing type "x"` {
		t.Fatalf("kernelError = %q", got)
	}
}
