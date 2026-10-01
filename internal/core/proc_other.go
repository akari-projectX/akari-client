//go:build !linux && !darwin && !windows

package core

import "os/exec"

func setParentDeath(*exec.Cmd) {}

func isOurKernel(int, string) bool { return false }
