// Package instance enforces a single running client per user with an
// exclusive, non-blocking lock on a file in the config dir. The OS drops
// the lock when the process dies, so a crash never leaves a stale lock.
package instance

import (
	"errors"
	"fmt"
	"os"
)

// ErrAlreadyRunning means another process holds the lock.
var ErrAlreadyRunning = errors.New("another instance of akari-client is already running")

// Lock is a held instance lock.
type Lock struct{ f *os.File }

// Acquire takes the lock at path or returns ErrAlreadyRunning.
func Acquire(path string) (*Lock, error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open lock file: %w", err)
	}
	if err := lockFile(f); err != nil {
		f.Close()
		if errors.Is(err, errWouldBlock) {
			return nil, ErrAlreadyRunning
		}
		return nil, fmt.Errorf("lock: %w", err)
	}
	_ = f.Truncate(0)
	_, _ = fmt.Fprintf(f, "%d\n", os.Getpid())
	return &Lock{f: f}, nil
}

// Release drops the lock.
func (l *Lock) Release() error {
	if l == nil || l.f == nil {
		return nil
	}
	_ = unlockFile(l.f)
	err := l.f.Close()
	l.f = nil
	return err
}
