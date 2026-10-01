package sysproxy

import (
	"context"
	"fmt"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

const internetSettings = `Software\Microsoft\Windows\CurrentVersion\Internet Settings`

// InternetSetOption options that make running WinINet clients reload.
const (
	internetOptionRefresh         = 37
	internetOptionSettingsChanged = 39
)

var (
	wininet               = windows.NewLazySystemDLL("wininet.dll")
	procInternetSetOption = wininet.NewProc("InternetSetOptionW")
)

// New returns the Windows manager (WinINet registry + refresh broadcast).
func New() Manager { return windowsMgr{} }

type windowsMgr struct{}

func notify() error {
	if err := procInternetSetOption.Find(); err != nil {
		return err
	}
	for _, opt := range []uintptr{internetOptionSettingsChanged, internetOptionRefresh} {
		r, _, err := procInternetSetOption.Call(0, opt, 0, 0)
		if r == 0 {
			return fmt.Errorf("InternetSetOption(%d): %w", opt, err)
		}
	}
	return nil
}

func (windowsMgr) Enable(_ context.Context, ep Endpoint) error {
	k, err := registry.OpenKey(registry.CURRENT_USER, internetSettings, registry.SET_VALUE)
	if err != nil {
		return fmt.Errorf("open Internet Settings: %w", err)
	}
	defer k.Close()
	s := WindowsSettings(ep)
	if err := k.SetStringValue("ProxyServer", s.ProxyServer); err != nil {
		return err
	}
	if err := k.SetStringValue("ProxyOverride", s.ProxyOverride); err != nil {
		return err
	}
	if err := k.SetDWordValue("ProxyEnable", s.ProxyEnable); err != nil {
		return err
	}
	return notify()
}

func (windowsMgr) Disable(_ context.Context, ep Endpoint) error {
	k, err := registry.OpenKey(registry.CURRENT_USER, internetSettings, registry.QUERY_VALUE|registry.SET_VALUE)
	if err != nil {
		return fmt.Errorf("open Internet Settings: %w", err)
	}
	defer k.Close()
	cur, _, err := k.GetStringValue("ProxyServer")
	if err != nil || !WindowsPointsAt(cur, ep) {
		return nil // not ours; leave the user's setting alone
	}
	if err := k.SetDWordValue("ProxyEnable", 0); err != nil {
		return err
	}
	return notify()
}
