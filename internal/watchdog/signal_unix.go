//go:build unix

package watchdog

import (
	"os"
	"syscall"
)

func interrupt(p *os.Process) error { return p.Signal(syscall.SIGTERM) }
