package device

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStableID(t *testing.T) {
	dir := t.TempDir()
	mid := func() string { return "machine-A" }
	a, err := Load(dir, mid, time.Now())
	if err != nil || !uuidRe.MatchString(a.ID) || a.HWHash == "" {
		t.Fatalf("%+v %v", a, err)
	}
	b, _ := Load(dir, mid, time.Now())
	if b.ID != a.ID {
		t.Fatal("id not stable")
	}
	// Copied to another machine -> new id.
	c, _ := Load(dir, func() string { return "machine-B" }, time.Now())
	if c.ID == a.ID {
		t.Fatal("id survived machine change")
	}
	// Machine id unavailable -> keep whatever is stored.
	d, _ := Load(dir, func() string { return "" }, time.Now())
	if d.ID != c.ID {
		t.Fatal("id changed when machine id unavailable")
	}
	// The raw machine id never hits the disk.
	raw, _ := os.ReadFile(filepath.Join(dir, FileName))
	if contains(raw, "machine-B") {
		t.Fatal("raw machine id persisted")
	}
}

func TestCorruptRegenerates(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, FileName), []byte(`{"id":"not-a-uuid"}`), 0o600)
	id, err := Load(dir, func() string { return "" }, time.Now())
	if err != nil || !uuidRe.MatchString(id.ID) {
		t.Fatalf("%+v %v", id, err)
	}
}

func TestUUIDv4(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 1000; i++ {
		u, err := NewUUID()
		if err != nil || !uuidRe.MatchString(u) || seen[u] {
			t.Fatalf("bad uuid %q %v", u, err)
		}
		seen[u] = true
	}
	if HashMachineID("") != "" || HashMachineID("X") != HashMachineID(" x\n") {
		t.Fatal("HashMachineID normalization")
	}
}

func contains(b []byte, s string) bool { return strings.Contains(string(b), s) }
