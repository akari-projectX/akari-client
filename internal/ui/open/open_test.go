package open

import (
	"reflect"
	"testing"
)

func TestCommand(t *testing.T) {
	if got := Command("windows", "C:\\x"); !reflect.DeepEqual(got, []string{"rundll32", "url.dll,FileProtocolHandler", "C:\\x"}) {
		t.Fatal(got)
	}
	if Command("darwin", "u")[0] != "open" || Command("linux", "u")[0] != "xdg-open" {
		t.Fatal("open command")
	}
}
