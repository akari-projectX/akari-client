package settings

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestPersistAndReload(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if g := s.Get(); g.Port() != DefaultMixedPort || g.Refresh() != DefaultRefresh || g.TestTarget() != DefaultTestURL {
		t.Fatalf("defaults: %+v", g)
	}
	if _, err := s.Update(func(x *Settings) {
		x.SubscriptionURL = "https://p/x/sub/t"
		x.MixedPort = 17890
		x.SelectedNode = "hk-1"
		x.ETag = `"e"`
		x.UserInfo = &UserInfo{Download: 5}
	}); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		fi, err := os.Stat(filepath.Join(dir, FileName))
		if err != nil || fi.Mode().Perm() != 0o600 {
			t.Fatalf("mode = %v, %v", fi.Mode().Perm(), err)
		}
	}
	s2, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	g := s2.Get()
	if g.SubscriptionURL != "https://p/x/sub/t" || g.Port() != 17890 || g.SelectedNode != "hk-1" || g.UserInfo.Download != 5 {
		t.Fatalf("reloaded: %+v", g)
	}
	// Get returns a copy.
	g.UserInfo.Download = 99
	if s2.Get().UserInfo.Download != 5 {
		t.Fatal("Get leaked internal pointer")
	}
}

func TestUpdateRejectsInvalid(t *testing.T) {
	s, _ := Open(t.TempDir())
	if _, err := s.Update(func(x *Settings) { x.MixedPort = 80 }); err == nil {
		t.Fatal("privileged port accepted")
	}
	if s.Get().MixedPort != 0 {
		t.Fatal("invalid value kept in memory")
	}
}

func TestRefreshPrecedence(t *testing.T) {
	if (Settings{PanelInterval: 24}).Refresh() != 24*time.Hour {
		t.Fatal("panel interval")
	}
	if (Settings{PanelInterval: 24, RefreshMinutes: 60}).Refresh() != time.Hour {
		t.Fatal("user override")
	}
	if (Settings{RefreshMinutes: 1}).Refresh() != MinRefresh {
		t.Fatal("min clamp")
	}
}

func TestCorruptFile(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, FileName), []byte("{"), 0o600)
	if _, err := Open(dir); err == nil {
		t.Fatal("corrupt settings accepted silently")
	}
}
