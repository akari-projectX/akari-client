// Package paths resolves the per-user directories the client writes to.
package paths

import (
	"fmt"
	"os"
	"path/filepath"
)

// AppDirName is the directory name under the OS config dir
// (%AppData% on Windows, ~/Library/Application Support on macOS,
// $XDG_CONFIG_HOME or ~/.config on Linux).
const AppDirName = "Akari"

// Dirs are the client's on-disk locations.
type Dirs struct {
	Root string // settings, profile, device id, lock
	Logs string
	Core string // mihomo home dir (cache files); never holds secrets of ours
}

// Resolve returns the directories rooted at override (if non-empty) or the
// OS config dir, creating them with owner-only permissions.
func Resolve(override string) (Dirs, error) {
	root := override
	if root == "" {
		base, err := os.UserConfigDir()
		if err != nil {
			return Dirs{}, fmt.Errorf("locate config dir: %w", err)
		}
		root = filepath.Join(base, AppDirName)
	}
	d := Dirs{Root: root, Logs: filepath.Join(root, "logs"), Core: filepath.Join(root, "core")}
	for _, p := range []string{d.Root, d.Logs, d.Core} {
		if err := os.MkdirAll(p, 0o700); err != nil {
			return Dirs{}, fmt.Errorf("create %s: %w", p, err)
		}
	}
	return d, nil
}
