package core

import (
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

func setParentDeath(*exec.Cmd) {}

func isOurKernel(pid int, bin string) bool {
	out, err := exec.Command("/bin/ps", "-o", "comm=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return false
	}
	comm := strings.TrimSpace(string(out))
	want := bin
	if r, err := filepath.EvalSymlinks(bin); err == nil {
		want = r
	}
	return comm == want || comm == bin
}
