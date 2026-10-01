// Package subscription fetches the user's Clash profile from the Akari
// panel (GET https://<host>/<prefix>/sub/<token>) and schedules refreshes.
package subscription

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
)

// tokenLen matches the panel's generated token: 32 random bytes,
// base64url without padding.
const tokenLen = 43

// ValidToken reports whether s looks like a panel subscription token.
func ValidToken(s string) bool {
	if len(s) != tokenLen {
		return false
	}
	for _, c := range []byte(s) {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

// Normalize turns user input into a canonical subscription URL. Accepted
// forms:
//
//	https://panel.example.com/<prefix>/sub/<token>   (subscription URL)
//	https://panel.example.com/<prefix>  + token      (panel URL + token)
//	https://panel.example.com/<prefix>/app + token   (copied from the browser)
//
// https is required except for loopback hosts (local development).
func Normalize(input, token string) (string, error) {
	input = strings.TrimSpace(input)
	token = strings.TrimSpace(token)
	if input == "" {
		return "", errors.New("subscription URL is empty")
	}
	u, err := url.Parse(input)
	if err != nil {
		return "", fmt.Errorf("invalid URL: %w", err)
	}
	if u.User != nil {
		return "", errors.New("URL must not contain credentials")
	}
	if u.Host == "" {
		return "", errors.New("URL has no host")
	}
	switch u.Scheme {
	case "https":
	case "http":
		if !isLoopback(u.Hostname()) {
			return "", errors.New("https is required (plain http only for localhost)")
		}
	default:
		return "", fmt.Errorf("unsupported scheme %q", u.Scheme)
	}
	u.Fragment = ""
	u.RawFragment = ""
	u.RawQuery = ""
	segs := splitPath(u.Path)

	if n := len(segs); n >= 2 && segs[n-2] == "sub" {
		if token != "" && token != segs[n-1] {
			return "", errors.New("URL already contains a different token")
		}
		if !ValidToken(segs[n-1]) {
			return "", errors.New("subscription token in URL is malformed")
		}
		if n < 3 {
			return "", errors.New("URL is missing the panel path prefix")
		}
		u.Path = "/" + strings.Join(segs, "/")
		u.RawPath = ""
		return u.String(), nil
	}
	if token == "" {
		return "", errors.New("enter the full subscription URL, or the panel URL and token")
	}
	if !ValidToken(token) {
		return "", errors.New("token is malformed")
	}
	if n := len(segs); n > 0 && segs[n-1] == "app" {
		segs = segs[:n-1]
	}
	if len(segs) == 0 {
		return "", errors.New("panel URL is missing its secret path prefix (https://host/<prefix>)")
	}
	segs = append(segs, "sub", token)
	u.Path = "/" + strings.Join(segs, "/")
	u.RawPath = ""
	return u.String(), nil
}

// Redact hides the path (secret prefix and token) of a subscription URL so
// it can be logged: https://panel.example.com/…
func Redact(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "<invalid url>"
	}
	return u.Scheme + "://" + u.Host + "/…"
}

func splitPath(p string) []string {
	var out []string
	for _, s := range strings.Split(p, "/") {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

func isLoopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
