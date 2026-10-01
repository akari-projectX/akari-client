package subscription

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	neturl "net/url"
	"strconv"
	"strings"
	"time"

	"github.com/akari-projectX/akari-client/internal/device"
	"github.com/akari-projectX/akari-client/internal/settings"
)

// MaxBodyBytes caps the profile size (the panel pads to 4 KiB buckets;
// thousands of nodes still fit comfortably).
const MaxBodyBytes = 8 << 20

// ErrRejected is returned when the panel answers 404 — its single
// rejection for an unknown/rotated token, a disabled or expired account,
// or a rate limit.
var ErrRejected = errors.New("subscription rejected by panel (token invalid or rotated, account disabled/expired, or rate limited)")

// Result is a successful fetch.
type Result struct {
	NotModified bool
	Body        []byte
	ETag        string
	UserInfo    *settings.UserInfo
	// IntervalHours is the panel's profile-update-interval (0 = absent).
	IntervalHours int
}

// Fetcher performs subscription requests.
type Fetcher struct {
	UserAgent string
	DeviceID  string
	// Direct is used first (no proxy, so a stale system proxy pointing at
	// a stopped core cannot break the fetch).
	Direct *http.Client
	// ViaCore, if it returns non-nil, is tried when the direct attempt
	// fails at the network level (panel unreachable without the proxy).
	ViaCore func() *http.Client
}

// NewDirectClient returns an HTTP client that never uses a proxy.
func NewDirectClient() *http.Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.Proxy = nil
	return &http.Client{Transport: tr, Timeout: 30 * time.Second}
}

// Fetch requests url, sending If-None-Match when etag is non-empty.
func (f *Fetcher) Fetch(ctx context.Context, url, etag string) (Result, error) {
	res, err := f.fetchWith(ctx, f.Direct, url, etag)
	if err == nil || !isNetworkError(err) || f.ViaCore == nil {
		return res, err
	}
	c := f.ViaCore()
	if c == nil {
		return res, err
	}
	res2, err2 := f.fetchWith(ctx, c, url, etag)
	if err2 != nil {
		return Result{}, fmt.Errorf("%w (via core: %v)", err, err2)
	}
	return res2, nil
}

type netError struct{ err error }

func (e netError) Error() string { return e.err.Error() }
func (e netError) Unwrap() error { return e.err }

func isNetworkError(err error) bool {
	var ne netError
	return errors.As(err, &ne)
}

func (f *Fetcher) fetchWith(ctx context.Context, c *http.Client, url, etag string) (Result, error) {
	if c == nil {
		c = NewDirectClient()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return Result{}, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("User-Agent", f.UserAgent)
	req.Header.Set("Accept", "text/yaml, application/yaml;q=0.9, */*;q=0.1")
	if f.DeviceID != "" {
		req.Header.Set(device.Header, f.DeviceID)
	}
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	resp, err := c.Do(req)
	if err != nil {
		// Strip the URL (it contains the token) from *url.Error.
		var ue *neturl.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return Result{}, netError{fmt.Errorf("fetch subscription: %w", err)}
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode == http.StatusNotModified:
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
		r := Result{NotModified: true, ETag: etag}
		r.UserInfo = ParseUserInfo(resp.Header.Get("Subscription-Userinfo"))
		r.IntervalHours = parseInterval(resp.Header.Get("Profile-Update-Interval"))
		if e := resp.Header.Get("ETag"); e != "" {
			r.ETag = e
		}
		return r, nil
	case resp.StatusCode == http.StatusNotFound:
		return Result{}, ErrRejected
	case resp.StatusCode != http.StatusOK:
		return Result{}, fmt.Errorf("subscription fetch: unexpected HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, MaxBodyBytes+1))
	if err != nil {
		return Result{}, netError{fmt.Errorf("read subscription: %w", err)}
	}
	if len(body) > MaxBodyBytes {
		return Result{}, fmt.Errorf("subscription larger than %d bytes", MaxBodyBytes)
	}
	if len(strings.TrimSpace(string(body))) == 0 {
		return Result{}, errors.New("subscription is empty")
	}
	return Result{
		Body:          body,
		ETag:          resp.Header.Get("ETag"),
		UserInfo:      ParseUserInfo(resp.Header.Get("Subscription-Userinfo")),
		IntervalHours: parseInterval(resp.Header.Get("Profile-Update-Interval")),
	}, nil
}

// ParseUserInfo parses "upload=0; download=123; total=456; expire=789".
// Returns nil when the header is absent or carries no known field.
func ParseUserInfo(h string) *settings.UserInfo {
	if strings.TrimSpace(h) == "" {
		return nil
	}
	var u settings.UserInfo
	seen := false
	for _, part := range strings.Split(h, ";") {
		k, v, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok {
			continue
		}
		n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
		if err != nil || n < 0 {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(k)) {
		case "upload":
			u.Upload, seen = n, true
		case "download":
			u.Download, seen = n, true
		case "total":
			u.Total, seen = n, true
		case "expire":
			u.Expire, seen = n, true
		}
	}
	if !seen {
		return nil
	}
	return &u
}

func parseInterval(h string) int {
	n, err := strconv.Atoi(strings.TrimSpace(h))
	if err != nil || n <= 0 || n > 24*30 {
		return 0
	}
	return n
}
