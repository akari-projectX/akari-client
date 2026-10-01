// Command akari-client is the Akari desktop client: a system-tray app that
// embeds the mihomo proxy kernel and exposes a local mixed (HTTP+SOCKS5)
// proxy on 127.0.0.1.
//
// Usage:
//
//	akari-client                       run (tray UI)
//	akari-client --headless            run without UI until SIGINT/SIGTERM
//	akari-client login <url> [token]   enroll a subscription, then exit
//	akari-client version
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/akari-projectX/akari-client/internal/app"
	"github.com/akari-projectX/akari-client/internal/clock"
	"github.com/akari-projectX/akari-client/internal/core"
	"github.com/akari-projectX/akari-client/internal/device"
	"github.com/akari-projectX/akari-client/internal/instance"
	"github.com/akari-projectX/akari-client/internal/logging"
	"github.com/akari-projectX/akari-client/internal/netwatch"
	"github.com/akari-projectX/akari-client/internal/paths"
	"github.com/akari-projectX/akari-client/internal/settings"
	"github.com/akari-projectX/akari-client/internal/subscription"
	"github.com/akari-projectX/akari-client/internal/sysproxy"
	"github.com/akari-projectX/akari-client/internal/ui/tray"
	"github.com/akari-projectX/akari-client/internal/ui/web"
	"github.com/akari-projectX/akari-client/internal/version"
)

// Exit codes.
const (
	exitOK             = 0
	exitAlreadyRunning = 3
	exitFatal          = 4
)

type options struct {
	dataDir  string
	headless bool
	logLevel string
	verbose  bool
}

func main() { os.Exit(run(os.Args[1:])) }

func run(args []string) int {
	fs := flag.NewFlagSet("akari-client", flag.ContinueOnError)
	var o options
	fs.StringVar(&o.dataDir, "data-dir", "", "settings/log directory (default: OS config dir/Akari)")
	fs.BoolVar(&o.headless, "headless", false, "run without the tray UI")
	fs.StringVar(&o.logLevel, "log-level", envOr("AKARI_LOG_LEVEL", "info"), "debug|info|warn|error")
	fs.BoolVar(&o.verbose, "v", false, "also log to stderr")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	switch fs.Arg(0) {
	case "version":
		fmt.Printf("akari-client %s (%s), mihomo %s\n", version.Version, version.Commit, core.MihomoVersion)
		return 0
	case "login":
		if fs.NArg() < 2 || fs.NArg() > 3 {
			fmt.Fprintln(os.Stderr, "usage: akari-client login <subscription-url | panel-url> [token]")
			return 2
		}
		return login(o, fs.Arg(1), fs.Arg(2))
	case "", "run":
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n", fs.Arg(0))
		return 2
	}
	return client(o)
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// setup resolves dirs and the logger.
func setup(o options, name string) (paths.Dirs, *slog.Logger, io.Closer, error) {
	dirs, err := paths.Resolve(o.dataDir)
	if err != nil {
		return dirs, nil, nil, err
	}
	logger, closer := logging.New(logging.Options{Dir: dirs.Logs, Level: logging.ParseLevel(o.logLevel), Stderr: o.verbose || o.headless})
	return dirs, logger.With("proc", name), closer, nil
}

type deps struct {
	dirs  paths.Dirs
	log   *slog.Logger
	app   *app.App
	close func()
}

func build(o options, name string) (*deps, int) {
	dirs, log, closer, err := setup(o, name)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return nil, exitFatal
	}
	lock, err := instance.Acquire(dirs.Root + string(os.PathSeparator) + "akari-client.lock")
	if err != nil {
		closer.Close()
		if errors.Is(err, instance.ErrAlreadyRunning) {
			fmt.Fprintln(os.Stderr, err)
			return nil, exitAlreadyRunning
		}
		fmt.Fprintln(os.Stderr, err)
		return nil, exitFatal
	}
	fail := func(err error) (*deps, int) {
		log.Error("startup", "err", err)
		fmt.Fprintln(os.Stderr, err)
		_ = lock.Release()
		closer.Close()
		return nil, exitFatal
	}
	dev, err := device.Load(dirs.Root, device.MachineID, time.Now())
	if err != nil {
		return fail(err)
	}
	st, err := settings.Open(dirs.Root)
	if err != nil {
		return fail(err)
	}
	bin, err := core.FindBinary()
	if err != nil {
		return fail(err)
	}
	if v, err := core.BinaryVersion(context.Background(), bin); err != nil {
		return fail(err)
	} else if v != core.MihomoVersion {
		log.Warn("unexpected kernel version", "have", v, "want", core.MihomoVersion)
	}
	eng, err := core.NewEngine(bin, dirs.Core, log)
	if err != nil {
		return fail(err)
	}
	a, err := app.New(app.Deps{
		Log:      log,
		Dir:      dirs.Root,
		Settings: st,
		Engine:   eng,
		Validate: eng.Validate,
		Fetcher: &subscription.Fetcher{
			UserAgent: version.UserAgent(),
			DeviceID:  dev.ID,
			Direct:    subscription.NewDirectClient(),
		},
		SysProxy: sysproxy.New(),
		Clock:    clock.Real{},
		Netwatch: &netwatch.Watcher{Clock: clock.Real{}},
		// Destination hosts appear in the kernel's info logs: only with -log-level debug.
		KernelLogLevel: map[bool]string{true: "debug", false: "warning"}[o.logLevel == "debug"],
	})
	if err != nil {
		return fail(err)
	}
	log.Info("starting", "version", version.Version, "commit", version.Commit, "mihomo", core.MihomoVersion)
	return &deps{dirs: dirs, log: log, app: a, close: func() {
		_ = lock.Release()
		closer.Close()
	}}, 0
}

func client(o options) int {
	d, code := build(o, "client")
	if d == nil {
		return code
	}
	defer d.close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	appCtx, cancelApp := context.WithCancel(ctx)
	appDone := make(chan struct{})
	go func() { d.app.Run(appCtx); close(appDone) }()

	settingsPage := web.New(d.app, d.log)
	defer settingsPage.Close()

	if o.headless {
		if !d.app.Status().Configured {
			d.log.Warn("not signed in; run `akari-client login <url>` first")
		}
		<-ctx.Done()
		cancelApp()
		<-appDone
		d.log.Info("stopped")
		return 0
	}

	go func() {
		<-ctx.Done()
		tray.Quit()
	}()
	tray.Run(tray.Deps{
		App:                 d.app,
		Settings:            settingsPage,
		LogDir:              d.dirs.Logs,
		Log:                 d.log,
		OpenSettingsOnStart: true,
	}, func() {
		cancelApp()
		<-appDone
		d.log.Info("stopped")
	})
	return 0
}

func login(o options, url, token string) int {
	d, code := build(o, "login")
	if d == nil {
		if code == exitAlreadyRunning {
			fmt.Fprintln(os.Stderr, "quit the running client first, or sign in from its Settings page")
		}
		return code
	}
	defer d.close()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := d.app.Enroll(ctx, url, token); err != nil {
		fmt.Fprintln(os.Stderr, "login failed:", err)
		return 1
	}
	st := d.app.Status()
	fmt.Printf("signed in to %s; start akari-client to connect\n", st.PanelHost)
	return 0
}
