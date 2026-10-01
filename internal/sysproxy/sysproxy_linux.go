package sysproxy

import (
	"context"
	"errors"
	"os"
	"os/exec"
)

// New returns the Linux manager (GNOME gsettings and/or KDE kioslaverc,
// best effort; other desktops are unsupported and return an error).
func New() Manager {
	return &linux{r: ExecRunner{}, d: DetectDesktop(os.Getenv("XDG_CURRENT_DESKTOP"), exec.LookPath)}
}

type linux struct {
	r Runner
	d Desktop
}

var errNoDesktop = errors.New("system proxy: no supported desktop settings backend (GNOME gsettings or KDE kwriteconfig) found; configure 127.0.0.1 manually")

func (l *linux) Enable(ctx context.Context, ep Endpoint) error {
	cmds := LinuxEnableCommands(l.d, ep)
	if len(cmds) == 0 {
		return errNoDesktop
	}
	return runAll(ctx, l.r, cmds)
}

func (l *linux) Disable(ctx context.Context, ep Endpoint) error {
	d := l.d
	if d.GNOME {
		mode, e1 := l.r.Run(ctx, "gsettings", "get", "org.gnome.system.proxy", "mode")
		host, e2 := l.r.Run(ctx, "gsettings", "get", "org.gnome.system.proxy.http", "host")
		port, e3 := l.r.Run(ctx, "gsettings", "get", "org.gnome.system.proxy.http", "port")
		if e1 == nil && e2 == nil && e3 == nil && !GnomePointsAt(string(mode), string(host), string(port), ep) {
			d.GNOME = false
		}
	}
	cmds := LinuxDisableCommands(d)
	if len(cmds) == 0 {
		return nil
	}
	return runAll(ctx, l.r, cmds)
}
