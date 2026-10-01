//go:build !linux && !windows && !darwin

package sysproxy

import (
	"context"
	"errors"
)

// New returns a manager that reports the platform as unsupported.
func New() Manager { return unsupported{} }

type unsupported struct{}

var errUnsupported = errors.New("system proxy not supported on this platform")

func (unsupported) Enable(context.Context, Endpoint) error  { return errUnsupported }
func (unsupported) Disable(context.Context, Endpoint) error { return nil }
