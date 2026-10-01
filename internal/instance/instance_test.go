package instance

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestSingleInstance(t *testing.T) {
	p := filepath.Join(t.TempDir(), "akari.lock")
	a, err := Acquire(p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Acquire(p); !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("second Acquire = %v", err)
	}
	if err := a.Release(); err != nil {
		t.Fatal(err)
	}
	b, err := Acquire(p)
	if err != nil {
		t.Fatalf("after release: %v", err)
	}
	_ = b.Release()
}
