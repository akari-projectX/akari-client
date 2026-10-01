// Package core runs the mihomo kernel as a separate process and controls it
// through mihomo's external-controller REST API. It is the only package
// that knows mihomo's config schema, CLI and API (none of which is a
// stable contract — see docs/DECISIONS.md D1). Pinned: v1.19.32.
//
// mihomo is GPL-3.0. It is shipped as an unmodified, separate executable
// next to akari-client and never linked into it.
package core

import (
	"bytes"
	"errors"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// MihomoVersion is the pinned kernel version (keep in sync with
// third_party/mihomo/go.mod).
const MihomoVersion = "v1.19.32"

// PreferredGroup is the selector the panel renders (sub.rs render_clash).
const PreferredGroup = "PROXY"

// Options are the client-controlled runtime settings.
type Options struct {
	MixedPort int
	// Controller is the 127.0.0.1:port of the REST API ("" = disabled,
	// used for validation runs).
	Controller string
	Secret     string
	LogLevel   string // debug|info|warning|error|silent (default info)
}

// Profile summarizes a sanitized subscription.
type Profile struct {
	Group string   // main selector group
	Nodes []string // members of Group
}

// builtin outbounds a group or rule may reference.
var builtin = map[string]bool{"DIRECT": true, "REJECT": true, "REJECT-DROP": true, "PASS": true, "COMPATIBLE": true, "GLOBAL": true}

type subscriptionDoc struct {
	Proxies []map[string]any `yaml:"proxies"`
	Groups  []map[string]any `yaml:"proxy-groups"`
	Rules   []string         `yaml:"rules"`
}

// BuildConfig turns a panel subscription into the mihomo config the client
// runs.
//
// Only proxies, proxy-groups and rules are taken from the subscription;
// every other key (listeners, external controller, TUN, DNS, providers,
// authentication, allow-lan, scripts, ...) is discarded and replaced by
// fixed client values, so a compromised or misconfigured panel cannot open
// a LAN listener or a control API on the user's machine. Group `use:`
// (proxy providers) is dropped as well: the client never fetches anything
// but the subscription itself.
func BuildConfig(subscription []byte, o Options) ([]byte, *Profile, error) {
	if o.MixedPort < 0 || o.MixedPort > 65535 {
		return nil, nil, fmt.Errorf("invalid mixed port %d", o.MixedPort)
	}
	var doc subscriptionDoc
	dec := yaml.NewDecoder(bytes.NewReader(subscription))
	if err := dec.Decode(&doc); err != nil {
		return nil, nil, fmt.Errorf("subscription is not a valid Clash profile: %w", err)
	}
	if len(doc.Proxies) == 0 {
		return nil, nil, errors.New("subscription contains no proxies (no nodes assigned to this account?)")
	}

	names := map[string]bool{}
	for i, p := range doc.Proxies {
		name, _ := p["name"].(string)
		typ, _ := p["type"].(string)
		if strings.TrimSpace(name) == "" || typ == "" {
			return nil, nil, fmt.Errorf("proxy #%d has no name or type", i+1)
		}
		if names[name] || builtin[name] {
			return nil, nil, fmt.Errorf("duplicate proxy name %q", name)
		}
		names[name] = true
	}
	groups := make([]map[string]any, 0, len(doc.Groups))
	groupMembers := map[string][]string{}
	for i, g := range doc.Groups {
		name, _ := g["name"].(string)
		typ, _ := g["type"].(string)
		if strings.TrimSpace(name) == "" || typ == "" {
			return nil, nil, fmt.Errorf("proxy group #%d has no name or type", i+1)
		}
		if names[name] || builtin[name] {
			return nil, nil, fmt.Errorf("duplicate proxy group name %q", name)
		}
		names[name] = true
		clean := map[string]any{}
		for k, v := range g {
			switch k {
			case "use", "include-all", "include-all-providers", "include-all-proxies", "filter", "exclude-filter", "exclude-type":
				continue // provider-based membership is not supported
			}
			clean[k] = v
		}
		members, ok := toStrings(g["proxies"])
		if !ok || len(members) == 0 {
			return nil, nil, fmt.Errorf("proxy group %q has no proxies", name)
		}
		groupMembers[name] = members
		groups = append(groups, clean)
	}
	for g, members := range groupMembers {
		for _, m := range members {
			if !names[m] && !builtin[m] {
				return nil, nil, fmt.Errorf("proxy group %q references unknown proxy %q", g, m)
			}
		}
	}

	prof := &Profile{Group: pickGroup(doc.Groups)}
	if prof.Group == "" {
		// No selector: expose all proxies through a synthetic one.
		prof.Group = PreferredGroup
		for names[prof.Group] {
			prof.Group += "_"
		}
		var all []string
		for _, p := range doc.Proxies {
			all = append(all, p["name"].(string))
		}
		groups = append([]map[string]any{{"name": prof.Group, "type": "select", "proxies": all}}, groups...)
		groupMembers[prof.Group] = all
	}
	prof.Nodes = groupMembers[prof.Group]

	rules := doc.Rules
	if len(rules) == 0 {
		rules = []string{"MATCH," + prof.Group}
	}
	level := o.LogLevel
	if level == "" {
		level = "info"
	}

	// Ordered output for readability of the on-disk file.
	out := yaml.Node{Kind: yaml.MappingNode}
	add := func(k string, v any) {
		var vn yaml.Node
		if err := vn.Encode(v); err != nil {
			panic(err) // only plain values are encoded
		}
		out.Content = append(out.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: k}, &vn)
	}
	add("mixed-port", o.MixedPort)
	add("allow-lan", false)
	add("bind-address", "127.0.0.1")
	add("mode", "rule")
	add("log-level", level)
	add("ipv6", true)
	add("unified-delay", true)
	add("find-process-mode", "off")
	add("geo-auto-update", false)
	add("external-controller", o.Controller)
	add("secret", o.Secret)
	add("profile", map[string]bool{"store-selected": false, "store-fake-ip": false})
	add("proxies", doc.Proxies)
	add("proxy-groups", groups)
	add("rules", rules)
	b, err := yaml.Marshal(&out)
	if err != nil {
		return nil, nil, fmt.Errorf("render config: %w", err)
	}
	return b, prof, nil
}

func toStrings(v any) ([]string, bool) {
	list, ok := v.([]any)
	if !ok {
		return nil, false
	}
	out := make([]string, 0, len(list))
	for _, x := range list {
		s, ok := x.(string)
		if !ok {
			return nil, false
		}
		out = append(out, s)
	}
	return out, true
}

func pickGroup(groups []map[string]any) string {
	first := ""
	for _, g := range groups {
		name, _ := g["name"].(string)
		typ, _ := g["type"].(string)
		if typ != "select" || name == "" {
			continue
		}
		if name == PreferredGroup {
			return name
		}
		if first == "" {
			first = name
		}
	}
	return first
}
