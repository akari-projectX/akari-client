package core

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/akari-projectX/akari-client/internal/store"
)

// Node is one selectable outbound of the main group.
type Node struct {
	Name string
	Type string
	// DelayMS is the last delay-test result for the test URL; 0 = untested,
	// -1 = failed.
	DelayMS int
}

const (
	configFile = "config.yaml"
	pidFile    = "mihomo.pid"
)

// BinaryName is the kernel executable shipped next to akari-client.
func BinaryName() string {
	if runtime.GOOS == "windows" {
		return "mihomo.exe"
	}
	return "mihomo"
}

// FindBinary locates the kernel: $AKARI_MIHOMO_BIN, else next to the
// running executable.
func FindBinary() (string, error) {
	if p := os.Getenv("AKARI_MIHOMO_BIN"); p != "" {
		if _, err := os.Stat(p); err != nil {
			return "", fmt.Errorf("AKARI_MIHOMO_BIN: %w", err)
		}
		return p, nil
	}
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if r, err := filepath.EvalSymlinks(exe); err == nil {
		exe = r
	}
	p := filepath.Join(filepath.Dir(exe), BinaryName())
	if _, err := os.Stat(p); err != nil {
		return "", fmt.Errorf("proxy kernel %s not found next to akari-client (reinstall the client)", BinaryName())
	}
	return p, nil
}

var versionRe = regexp.MustCompile(`\bv\d+\.\d+\.\d+\S*`)

// BinaryVersion runs `mihomo -v`.
func BinaryVersion(ctx context.Context, bin string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "-v")
	hideWindow(cmd)
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("%s -v: %w", filepath.Base(bin), err)
	}
	v := versionRe.FindString(string(out))
	if v == "" {
		return "", fmt.Errorf("unrecognized version output %q", strings.TrimSpace(string(out)))
	}
	return v, nil
}

// Engine supervises one mihomo process. Methods are safe for concurrent use.
type Engine struct {
	bin  string
	home string
	log  *slog.Logger

	mu    sync.Mutex
	proc  *process
	api   *apiClient
	port  int
	group string
}

type process struct {
	cmd  *exec.Cmd
	done chan struct{}
	err  error // valid after done is closed

	tailMu sync.Mutex
	tail   []string // last lines of output, for error messages
}

func (p *process) alive() bool {
	select {
	case <-p.done:
		return false
	default:
		return true
	}
}

func (p *process) lastLines() string {
	p.tailMu.Lock()
	defer p.tailMu.Unlock()
	return strings.Join(p.tail, " | ")
}

// NewEngine returns an engine running bin with home as its working
// directory (config file, cache, pid file; created 0700).
func NewEngine(bin, home string, log *slog.Logger) (*Engine, error) {
	if err := os.MkdirAll(home, 0o700); err != nil {
		return nil, err
	}
	return &Engine{bin: bin, home: home, log: log}, nil
}

func randomSecret() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

func freeLoopbackPort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

// Validate checks a subscription with the kernel itself (`mihomo -t`)
// without touching the running instance.
func (e *Engine) Validate(subscription []byte) error {
	cfg, _, err := BuildConfig(subscription, Options{})
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(e.home, "validate-*.yaml")
	if err != nil {
		return err
	}
	path := f.Name()
	defer os.Remove(path)
	_ = f.Chmod(0o600)
	if _, err := f.Write(cfg); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, e.bin, "-t", "-d", e.home, "-f", path)
	hideWindow(cmd)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("profile rejected by kernel: %s", kernelError(string(out), err))
	}
	return nil
}

var logLineRe = regexp.MustCompile(`level=(\w+) msg="((?:[^"\\]|\\.)*)"`)

// kernelError extracts the most relevant line of mihomo output.
func kernelError(out string, err error) string {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if m := logLineRe.FindStringSubmatch(lines[i]); m != nil && (m[1] == "error" || m[1] == "fatal") {
			return unquote(m[2])
		}
	}
	for i := len(lines) - 1; i >= 0; i-- {
		if s := strings.TrimSpace(lines[i]); s != "" && !strings.Contains(s, "level=info") {
			return s
		}
	}
	return err.Error()
}

func unquote(s string) string {
	if u, err := strconv.Unquote(`"` + s + `"`); err == nil {
		return u
	}
	return s
}

// Start runs the kernel with subscription on 127.0.0.1:opts.MixedPort.
// While running, Start reloads the new config in place (existing
// connections through unchanged nodes survive).
func (e *Engine) Start(subscription []byte, opts Options) error {
	if opts.MixedPort <= 0 {
		return fmt.Errorf("invalid mixed port %d", opts.MixedPort)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.proc != nil && e.proc.alive() {
		return e.reloadLocked(subscription, opts)
	}
	e.proc = nil
	return e.spawnLocked(subscription, opts)
}

func (e *Engine) reloadLocked(subscription []byte, opts Options) error {
	opts.Controller, opts.Secret = e.api.addr, e.api.secret
	cfg, prof, err := BuildConfig(subscription, opts)
	if err != nil {
		return err
	}
	next := filepath.Join(e.home, "config.next.yaml")
	if err := store.WriteFile(next, cfg); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := e.api.reload(ctx, next); err != nil {
		_ = os.Remove(next)
		return fmt.Errorf("reload: %w", err)
	}
	if err := os.Rename(next, filepath.Join(e.home, configFile)); err != nil {
		return err
	}
	e.group = prof.Group
	if err := e.checkPortLocked(ctx, opts.MixedPort); err != nil {
		e.stopLocked()
		return err
	}
	e.port = opts.MixedPort
	return nil
}

func (e *Engine) checkPortLocked(ctx context.Context, want int) error {
	got, err := e.api.mixedPort(ctx)
	if err != nil {
		return err
	}
	if got != want {
		return fmt.Errorf("could not listen on 127.0.0.1:%d (port in use?)", want)
	}
	return nil
}

func (e *Engine) spawnLocked(subscription []byte, opts Options) error {
	e.killOrphan()
	port, err := freeLoopbackPort()
	if err != nil {
		return err
	}
	secret, err := randomSecret()
	if err != nil {
		return err
	}
	opts.Controller = net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	opts.Secret = secret
	cfg, prof, err := BuildConfig(subscription, opts)
	if err != nil {
		return err
	}
	cfgPath := filepath.Join(e.home, configFile)
	if err := store.WriteFile(cfgPath, cfg); err != nil {
		return err
	}

	cmd := exec.Command(e.bin, "-d", e.home, "-f", cfgPath)
	cmd.Env = kernelEnv()
	hideWindow(cmd)
	setParentDeath(cmd)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	cmd.Stderr = cmd.Stdout
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start kernel: %w", err)
	}
	if err := bindToParent(cmd.Process); err != nil {
		e.log.Warn("kernel not bound to client lifetime", "err", err)
	}
	_ = store.WriteFile(filepath.Join(e.home, pidFile), []byte(strconv.Itoa(cmd.Process.Pid)))
	p := &process{cmd: cmd, done: make(chan struct{})}
	go e.pump(p, stdout)
	go func() {
		p.err = cmd.Wait()
		close(p.done)
	}()

	api := newAPIClient(opts.Controller, secret)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := api.waitReady(ctx, p.done); err != nil {
		e.killLocked(p)
		if tail := p.lastLines(); tail != "" {
			return fmt.Errorf("kernel failed to start: %s", kernelError(strings.ReplaceAll(tail, " | ", "\n"), err))
		}
		return fmt.Errorf("kernel failed to start: %w", err)
	}
	e.proc, e.api, e.group = p, api, prof.Group
	if err := e.checkPortLocked(ctx, opts.MixedPort); err != nil {
		e.stopLocked()
		return err
	}
	e.port = opts.MixedPort
	return nil
}

// kernelEnv drops variables that would reconfigure the kernel behind our
// back (CLASH_* overrides, SAFE_PATHS, proxy settings).
func kernelEnv() []string {
	var env []string
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		ku := strings.ToUpper(k)
		if strings.HasPrefix(ku, "CLASH_") || strings.HasPrefix(ku, "MIHOMO_") || ku == "SAFE_PATHS" ||
			ku == "HTTP_PROXY" || ku == "HTTPS_PROXY" || ku == "ALL_PROXY" || ku == "NO_PROXY" {
			continue
		}
		env = append(env, kv)
	}
	return env
}

// pump forwards kernel output to the structured log.
func (e *Engine) pump(p *process, r io.Reader) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		p.tailMu.Lock()
		p.tail = append(p.tail, line)
		if len(p.tail) > 20 {
			p.tail = p.tail[1:]
		}
		p.tailMu.Unlock()
		lvl, msg := slog.LevelInfo, line
		if m := logLineRe.FindStringSubmatch(line); m != nil {
			msg = unquote(m[2])
			switch m[1] {
			case "debug":
				lvl = slog.LevelDebug
			case "warning", "warn":
				lvl = slog.LevelWarn
			case "error", "fatal", "panic":
				lvl = slog.LevelError
			}
		}
		e.log.Log(context.Background(), lvl, msg, "src", "mihomo")
	}
}

// Stop terminates the kernel (graceful first, then kill).
func (e *Engine) Stop() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.stopLocked()
	return nil
}

func (e *Engine) stopLocked() {
	if e.proc != nil {
		e.killLocked(e.proc)
	}
	e.proc, e.api, e.port = nil, nil, 0
	_ = os.Remove(filepath.Join(e.home, pidFile))
}

func (e *Engine) killLocked(p *process) {
	if !p.alive() {
		return
	}
	_ = terminate(p.cmd.Process)
	select {
	case <-p.done:
		return
	case <-time.After(5 * time.Second):
	}
	_ = p.cmd.Process.Kill()
	<-p.done
}

// killOrphan kills a kernel left running by a previous client instance
// that died without stopping it (pid file in our private home dir).
func (e *Engine) killOrphan() {
	path := filepath.Join(e.home, pidFile)
	b, err := os.ReadFile(path)
	if err != nil {
		return
	}
	_ = os.Remove(path)
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || pid <= 0 || pid == os.Getpid() {
		return
	}
	if !isOurKernel(pid, e.bin) {
		return
	}
	if p, err := os.FindProcess(pid); err == nil {
		e.log.Warn("killing orphaned kernel from a previous run", "pid", pid)
		_ = p.Kill()
		// Give the OS a moment to release the ports.
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) && isOurKernel(pid, e.bin) {
			time.Sleep(50 * time.Millisecond)
		}
	}
}

var closedChan = func() chan struct{} { c := make(chan struct{}); close(c); return c }()

// Exited returns a channel closed when the current kernel process exits
// (already closed when none is running).
func (e *Engine) Exited() <-chan struct{} {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.proc == nil {
		return closedChan
	}
	return e.proc.done
}

// Running reports whether the kernel process is alive.
func (e *Engine) Running() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.proc != nil && e.proc.alive()
}

func (e *Engine) client() (*apiClient, string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.proc == nil || !e.proc.alive() {
		return nil, "", errors.New("core not running")
	}
	return e.api, e.group, nil
}

// Healthy checks the process, its API and the mixed port.
func (e *Engine) Healthy(ctx context.Context) error {
	e.mu.Lock()
	p, api, port := e.proc, e.api, e.port
	e.mu.Unlock()
	if p == nil {
		return errors.New("core not running")
	}
	if !p.alive() {
		return fmt.Errorf("kernel exited: %v", p.err)
	}
	if err := api.ping(ctx); err != nil {
		return fmt.Errorf("kernel API: %w", err)
	}
	return ProbeSOCKS(ctx, net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
}

// ProbeSOCKS performs a no-auth SOCKS5 greeting against addr.
func ProbeSOCKS(ctx context.Context, addr string) error {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	var d net.Dialer
	c, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("dial mixed port: %w", err)
	}
	defer c.Close()
	if dl, ok := ctx.Deadline(); ok {
		_ = c.SetDeadline(dl)
	}
	if _, err := c.Write([]byte{5, 1, 0}); err != nil {
		return fmt.Errorf("socks greeting: %w", err)
	}
	var resp [2]byte
	if _, err := io.ReadFull(c, resp[:]); err != nil {
		return fmt.Errorf("socks greeting: %w", err)
	}
	if resp[0] != 5 || resp[1] != 0 {
		return fmt.Errorf("unexpected socks reply %x", resp)
	}
	return nil
}

// Nodes lists the main group's members, its selection and each member's
// last delay to testURL.
func (e *Engine) Nodes(testURL string) (string, []Node, error) {
	api, group, err := e.client()
	if err != nil {
		return "", nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	all, err := api.proxies(ctx)
	if err != nil {
		return "", nil, err
	}
	g, ok := all[group]
	if !ok {
		return "", nil, fmt.Errorf("group %q not found", group)
	}
	nodes := make([]Node, 0, len(g.All))
	for _, name := range g.All {
		p := all[name]
		n := Node{Name: name, Type: p.Type}
		if st, ok := p.Extra[testURL]; ok && len(st.History) > 0 {
			last := st.History[len(st.History)-1]
			if !st.Alive || last.Delay == 0 {
				n.DelayMS = -1
			} else {
				n.DelayMS = last.Delay
			}
		}
		nodes = append(nodes, n)
	}
	return g.Now, nodes, nil
}

// Select switches the main group to node and closes existing connections
// so the change takes effect immediately.
func (e *Engine) Select(node string) error {
	api, group, err := e.client()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	all, err := api.proxies(ctx)
	if err != nil {
		return err
	}
	if all[group].Now == node {
		return nil
	}
	if err := api.selectProxy(ctx, group, node); err != nil {
		return fmt.Errorf("select %q: %w", node, err)
	}
	_ = api.closeConnections(ctx)
	return nil
}

func timeoutMS(ctx context.Context, def time.Duration) int {
	d := def
	if dl, ok := ctx.Deadline(); ok {
		if r := time.Until(dl) - 200*time.Millisecond; r > 0 && r < d {
			d = r
		}
	}
	return max(int(d/time.Millisecond), 100)
}

// DelayTest measures one node against testURL (mihomo URL test).
func (e *Engine) DelayTest(ctx context.Context, node, testURL string) (time.Duration, error) {
	api, _, err := e.client()
	if err != nil {
		return 0, err
	}
	ms, err := api.delay(ctx, node, testURL, timeoutMS(ctx, 5*time.Second))
	if err != nil {
		return 0, err
	}
	return time.Duration(max(ms, 1)) * time.Millisecond, nil
}

// DelayTestAll tests every node of the main group (failed = -1).
func (e *Engine) DelayTestAll(ctx context.Context, testURL string, timeout time.Duration) map[string]time.Duration {
	out := map[string]time.Duration{}
	api, group, err := e.client()
	if err != nil {
		return out
	}
	_, nodes, err := e.Nodes(testURL)
	if err != nil {
		return out
	}
	res, err := api.groupDelay(ctx, group, testURL, int(timeout/time.Millisecond))
	for _, n := range nodes {
		if ms, ok := res[n.Name]; ok && err == nil && ms > 0 {
			out[n.Name] = time.Duration(ms) * time.Millisecond
		} else {
			out[n.Name] = -1
		}
	}
	return out
}

// ResetNetwork drops all proxied connections and the kernel's DNS cache —
// used after a network change or resume, when existing sockets are dead.
func (e *Engine) ResetNetwork() {
	api, _, err := e.client()
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = api.closeConnections(ctx)
	_ = api.flushDNS(ctx)
}
