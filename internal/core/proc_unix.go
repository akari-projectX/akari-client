//go:build unix

package core

import (
	"os"
	"os/exec"
	"syscall"
)

func hideWindow(*exec.Cmd) {}

func terminate(p *os.Process) error { return p.Signal(syscall.SIGTERM) }

// bindToParent: on Linux the kernel gets SIGKILL when the client dies
// (setParentDeath); elsewhere the pid file + killOrphan covers it.
func bindToParent(*os.Process) error { return nil }
