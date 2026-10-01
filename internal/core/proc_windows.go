package core

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

func hideWindow(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.HideWindow = true
	cmd.SysProcAttr.CreationFlags |= windows.CREATE_NO_WINDOW
}

func setParentDeath(*exec.Cmd) {}

// There is no SIGTERM for a windowless console process; mihomo holds no
// OS state that needs a graceful shutdown in this configuration (no TUN,
// no system DNS changes).
func terminate(p *os.Process) error { return p.Kill() }

var (
	jobOnce sync.Once
	job     windows.Handle
	jobErr  error
)

// bindToParent puts the kernel in a job object that is killed when the
// client's last handle to it closes, i.e. when the client exits or dies.
func bindToParent(p *os.Process) error {
	jobOnce.Do(func() {
		job, jobErr = windows.CreateJobObject(nil, nil)
		if jobErr != nil {
			return
		}
		info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
		info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
		_, jobErr = windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
			uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info)))
	})
	if jobErr != nil {
		return jobErr
	}
	h, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(p.Pid))
	if err != nil {
		return err
	}
	defer windows.CloseHandle(h)
	return windows.AssignProcessToJobObject(job, h)
}

func isOurKernel(pid int, bin string) bool {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	defer windows.CloseHandle(h)
	var exit uint32
	if err := windows.GetExitCodeProcess(h, &exit); err != nil || exit != 259 { // STILL_ACTIVE
		return false
	}
	buf := make([]uint16, windows.MAX_LONG_PATH)
	n := uint32(len(buf))
	if err := windows.QueryFullProcessImageName(h, 0, &buf[0], &n); err != nil {
		return false
	}
	want, err := filepath.Abs(bin)
	if err != nil {
		want = bin
	}
	return strings.EqualFold(windows.UTF16ToString(buf[:n]), want)
}
