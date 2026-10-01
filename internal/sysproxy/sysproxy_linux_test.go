package sysproxy

import (
	"context"
	"strings"
	"testing"
)

type fakeRunner struct {
	ran     [][]string
	answers map[string]string
}

func (f *fakeRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	c := append([]string{name}, args...)
	f.ran = append(f.ran, c)
	return []byte(f.answers[strings.Join(c, " ")]), nil
}

func TestLinuxDisableOnlyIfOurs(t *testing.T) {
	r := &fakeRunner{answers: map[string]string{
		"gsettings get org.gnome.system.proxy mode":      "'manual'\n",
		"gsettings get org.gnome.system.proxy.http host": "'corp.example'\n",
		"gsettings get org.gnome.system.proxy.http port": "8080\n",
	}}
	l := &linux{r: r, d: Desktop{GNOME: true}}
	if err := l.Disable(context.Background(), ep); err != nil {
		t.Fatal(err)
	}
	for _, c := range r.ran {
		if c[1] == "set" {
			t.Fatalf("changed a foreign proxy setting: %q", c)
		}
	}
	r.answers["gsettings get org.gnome.system.proxy.http host"] = "'127.0.0.1'\n"
	r.answers["gsettings get org.gnome.system.proxy.http port"] = "7890\n"
	r.ran = nil
	_ = l.Disable(context.Background(), ep)
	if last := r.ran[len(r.ran)-1]; strings.Join(last, " ") != "gsettings set org.gnome.system.proxy mode none" {
		t.Fatalf("last = %q", last)
	}
	if err := (&linux{r: r}).Enable(context.Background(), ep); err == nil {
		t.Fatal("enable without backend must error")
	}
}
