package sysproxy

import (
	"context"
	"errors"
)

// New returns the macOS manager (networksetup over all enabled services).
func New() Manager { return &darwin{r: ExecRunner{}} }

type darwin struct{ r Runner }

func (d *darwin) services(ctx context.Context) ([]string, error) {
	out, err := d.r.Run(ctx, networksetup, "-listallnetworkservices")
	if err != nil {
		return nil, err
	}
	s := ParseDarwinServices(string(out))
	if len(s) == 0 {
		return nil, errors.New("no enabled network services")
	}
	return s, nil
}

func (d *darwin) Enable(ctx context.Context, ep Endpoint) error {
	s, err := d.services(ctx)
	if err != nil {
		return err
	}
	return runAll(ctx, d.r, DarwinEnableCommands(s, ep))
}

func (d *darwin) Disable(ctx context.Context, ep Endpoint) error {
	s, err := d.services(ctx)
	if err != nil {
		return err
	}
	var ours []string
	for _, svc := range s {
		out, err := d.r.Run(ctx, networksetup, "-getwebproxy", svc)
		if err == nil && DarwinPointsAt(string(out), ep) {
			ours = append(ours, svc)
		}
	}
	return runAll(ctx, d.r, DarwinDisableCommands(ours))
}
