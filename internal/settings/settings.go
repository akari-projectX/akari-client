// Package settings holds the user's persisted preferences and the small
// amount of runtime state that must survive restarts.
//
// The subscription URL embeds the user's subscription token, which is a
// credential: settings.json is written 0600 and never logged (see
// subscription.Redact).
package settings

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/akari-projectX/akari-client/internal/store"
)

const (
	FileName = "settings.json"

	DefaultMixedPort = 7890
	DefaultTestURL   = "https://www.gstatic.com/generate_204"

	// MinRefresh bounds how often the subscription is re-fetched
	// automatically (the panel rate-limits subscription fetches).
	MinRefresh = 15 * time.Minute
	// DefaultRefresh applies when neither the user nor the panel
	// (profile-update-interval) specifies an interval.
	DefaultRefresh = 12 * time.Hour
)

// UserInfo mirrors the panel's subscription-userinfo header.
type UserInfo struct {
	Upload   int64 `json:"upload"`
	Download int64 `json:"download"`
	Total    int64 `json:"total"`  // 0 = unlimited
	Expire   int64 `json:"expire"` // unix seconds, 0 = never
}

// Settings is the persisted document. Zero values mean "default".
type Settings struct {
	SubscriptionURL string `json:"subscription_url,omitempty"`
	MixedPort       int    `json:"mixed_port,omitempty"`
	// RefreshMinutes overrides the panel's profile-update-interval.
	RefreshMinutes int    `json:"refresh_minutes,omitempty"`
	TestURL        string `json:"test_url,omitempty"`
	// Connect is the user's desired core state (restored on launch).
	Connect     bool `json:"connect"`
	SystemProxy bool `json:"system_proxy"`

	SelectedNode string `json:"selected_node,omitempty"`

	ETag          string    `json:"etag,omitempty"`
	LastFetch     time.Time `json:"last_fetch,omitempty"`
	PanelInterval int       `json:"panel_interval_hours,omitempty"`
	UserInfo      *UserInfo `json:"user_info,omitempty"`
}

// Port returns the effective mixed port.
func (s Settings) Port() int {
	if s.MixedPort > 0 && s.MixedPort < 65536 {
		return s.MixedPort
	}
	return DefaultMixedPort
}

// TestTarget returns the effective delay-test URL.
func (s Settings) TestTarget() string {
	if s.TestURL != "" {
		return s.TestURL
	}
	return DefaultTestURL
}

// Refresh returns the effective automatic refresh interval.
func (s Settings) Refresh() time.Duration {
	var d time.Duration
	switch {
	case s.RefreshMinutes > 0:
		d = time.Duration(s.RefreshMinutes) * time.Minute
	case s.PanelInterval > 0:
		d = time.Duration(s.PanelInterval) * time.Hour
	default:
		d = DefaultRefresh
	}
	if d < MinRefresh {
		d = MinRefresh
	}
	return d
}

// Validate rejects values the client cannot run with.
func (s Settings) Validate() error {
	if s.MixedPort != 0 && (s.MixedPort < 1024 || s.MixedPort > 65535) {
		return fmt.Errorf("mixed port %d out of range 1024-65535", s.MixedPort)
	}
	if s.RefreshMinutes < 0 {
		return errors.New("refresh interval must be positive")
	}
	return nil
}

// Store is a concurrency-safe handle on settings.json.
type Store struct {
	path string
	mu   sync.Mutex
	cur  Settings
}

// Open loads dir/settings.json (missing = defaults).
func Open(dir string) (*Store, error) {
	s := &Store{path: filepath.Join(dir, FileName)}
	err := store.ReadJSON(s.path, &s.cur)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	return s, nil
}

// Get returns a copy of the current settings.
func (s *Store) Get() Settings {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.cur
	if c.UserInfo != nil {
		u := *c.UserInfo
		c.UserInfo = &u
	}
	return c
}

// Update applies fn to a copy, validates and persists it. The in-memory
// value only changes when the write succeeds.
func (s *Store) Update(fn func(*Settings)) (Settings, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := s.cur
	fn(&next)
	if err := next.Validate(); err != nil {
		return s.cur, err
	}
	if err := store.WriteJSON(s.path, next); err != nil {
		return s.cur, err
	}
	s.cur = next
	return next, nil
}
