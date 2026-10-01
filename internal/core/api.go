package core

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// apiClient talks to mihomo's external controller on 127.0.0.1 with a
// per-process random secret.
type apiClient struct {
	addr   string
	secret string
	hc     *http.Client
}

func newAPIClient(addr, secret string) *apiClient {
	tr := &http.Transport{Proxy: nil, MaxIdleConns: 4, IdleConnTimeout: 30 * time.Second}
	return &apiClient{addr: addr, secret: secret, hc: &http.Client{Transport: tr, Timeout: 60 * time.Second}}
}

type apiError struct {
	Status  int
	Message string
}

func (e *apiError) Error() string {
	if e.Message != "" {
		return e.Message
	}
	return "HTTP " + strconv.Itoa(e.Status)
}

func (c *apiClient) do(ctx context.Context, method, path string, query url.Values, body, out any) error {
	u := "http://" + c.addr + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.secret)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode >= 300 {
		var m struct {
			Message string `json:"message"`
		}
		_ = json.Unmarshal(data, &m)
		return &apiError{Status: resp.StatusCode, Message: m.Message}
	}
	if out != nil && len(data) > 0 {
		return json.Unmarshal(data, out)
	}
	return nil
}

func (c *apiClient) ping(ctx context.Context) error {
	var v struct {
		Version string `json:"version"`
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	return c.do(ctx, http.MethodGet, "/version", nil, nil, &v)
}

// waitReady polls /version until it answers, the process exits, or ctx
// expires.
func (c *apiClient) waitReady(ctx context.Context, exited <-chan struct{}) error {
	for {
		if err := c.ping(ctx); err == nil {
			return nil
		}
		select {
		case <-exited:
			return errors.New("kernel exited during startup")
		case <-ctx.Done():
			return errors.New("kernel API did not come up")
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func (c *apiClient) reload(ctx context.Context, path string) error {
	return c.do(ctx, http.MethodPut, "/configs", url.Values{"force": {"true"}}, map[string]string{"path": path}, nil)
}

func (c *apiClient) mixedPort(ctx context.Context) (int, error) {
	var v struct {
		MixedPort int `json:"mixed-port"`
	}
	err := c.do(ctx, http.MethodGet, "/configs", nil, nil, &v)
	return v.MixedPort, err
}

type proxyInfo struct {
	Type  string   `json:"type"`
	Now   string   `json:"now"`
	All   []string `json:"all"`
	Extra map[string]struct {
		Alive   bool `json:"alive"`
		History []struct {
			Delay int `json:"delay"`
		} `json:"history"`
	} `json:"extra"`
}

func (c *apiClient) proxies(ctx context.Context) (map[string]proxyInfo, error) {
	var v struct {
		Proxies map[string]proxyInfo `json:"proxies"`
	}
	err := c.do(ctx, http.MethodGet, "/proxies", nil, nil, &v)
	return v.Proxies, err
}

func (c *apiClient) selectProxy(ctx context.Context, group, node string) error {
	return c.do(ctx, http.MethodPut, "/proxies/"+url.PathEscape(group), nil, map[string]string{"name": node}, nil)
}

func (c *apiClient) delay(ctx context.Context, node, testURL string, timeoutMS int) (int, error) {
	var v struct {
		Delay int `json:"delay"`
	}
	q := url.Values{"url": {testURL}, "timeout": {strconv.Itoa(timeoutMS)}}
	if err := c.do(ctx, http.MethodGet, "/proxies/"+url.PathEscape(node)+"/delay", q, nil, &v); err != nil {
		return 0, fmt.Errorf("delay test %q: %w", node, err)
	}
	return v.Delay, nil
}

func (c *apiClient) groupDelay(ctx context.Context, group, testURL string, timeoutMS int) (map[string]int, error) {
	v := map[string]int{}
	q := url.Values{"url": {testURL}, "timeout": {strconv.Itoa(timeoutMS)}}
	err := c.do(ctx, http.MethodGet, "/group/"+url.PathEscape(group)+"/delay", q, nil, &v)
	return v, err
}

func (c *apiClient) closeConnections(ctx context.Context) error {
	return c.do(ctx, http.MethodDelete, "/connections", nil, nil, nil)
}

func (c *apiClient) flushDNS(ctx context.Context) error {
	return c.do(ctx, http.MethodPost, "/cache/dns/flush", nil, nil, nil)
}
