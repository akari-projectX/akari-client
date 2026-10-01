package core

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

func setParentDeath(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Pdeathsig = syscall.SIGKILL
}

func isOurKernel(pid int, bin string) bool {
	exe, err := os.Readlink("/proc/" + strconv.Itoa(pid) + "/exe")
	if err != nil {
		return false
	}
	exe = strings.TrimSuffix(exe, " (deleted)")
	want := bin
	if r, err := filepath.EvalSymlinks(bin); err == nil {
		want = r
	}
	if abs, err := filepath.Abs(want); err == nil {
		want = abs
	}
	return exe == want
}
