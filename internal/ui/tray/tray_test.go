package tray

import (
	"testing"

	"github.com/akari-projectX/akari-client/internal/core"
)

func TestNodeLabel(t *testing.T) {
	for n, want := range map[core.Node]string{
		{Name: "hk-1"}:              "hk-1",
		{Name: "hk-1", DelayMS: 42}: "hk-1    42 ms",
		{Name: "hk-1", DelayMS: -1}: "hk-1    timeout",
	} {
		if got := NodeLabel(n); got != want {
			t.Errorf("NodeLabel(%+v) = %q", n, got)
		}
	}
	if trim("abcdef", 4) != "abc…" || trim("ab", 4) != "ab" {
		t.Fatal("trim")
	}
}
