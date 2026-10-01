package logging

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestJSONFileLog(t *testing.T) {
	dir := t.TempDir()
	l, c := New(Options{Dir: dir, Level: slog.LevelInfo})
	l.Debug("hidden")
	l.Info("hello", "k", 1)
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, FileName))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(lines) != 1 {
		t.Fatalf("lines = %q", lines)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &m); err != nil || m["msg"] != "hello" || m["k"] != float64(1) {
		t.Fatalf("record = %v (%v)", m, err)
	}
}

func TestParseLevel(t *testing.T) {
	if ParseLevel("DEBUG") != slog.LevelDebug || ParseLevel("warning") != slog.LevelWarn || ParseLevel("x") != slog.LevelInfo {
		t.Fatal("ParseLevel")
	}
}
