// Package open opens a URL or folder with the desktop's default handler.
package open

import (
	"context"
	"os/exec"
	"runtime"
	"time"
)

// Command returns the command line that opens target on goos.
func Command(goos, target string) []string {
	switch goos {
	case "windows":
		return []string{"rundll32", "url.dll,FileProtocolHandler", target}
	case "darwin":
		return []string{"open", target}
	default:
		return []string{"xdg-open", target}
	}
}

// Open launches the default handler for target (a URL or a directory).
func Open(target string) error {
	c := Command(runtime.GOOS, target)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, c[0], c[1:]...)
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}
