package icon

import (
	"bytes"
	"encoding/binary"
	"image/png"
	"testing"
)

func TestPNGAndICO(t *testing.T) {
	p := PNG(32, Connected)
	img, err := png.Decode(bytes.NewReader(p))
	if err != nil || img.Bounds().Dx() != 32 {
		t.Fatalf("png: %v", err)
	}
	if _, _, _, a := img.At(16, 16).RGBA(); a == 0 {
		t.Fatal("center transparent")
	}
	if _, _, _, a := img.At(0, 0).RGBA(); a != 0 {
		t.Fatal("corner opaque")
	}
	ico := ForOS("windows", Disconnected)
	var hdr [3]uint16
	_ = binary.Read(bytes.NewReader(ico), binary.LittleEndian, &hdr)
	if hdr != [3]uint16{0, 1, 2} {
		t.Fatalf("ico header %v", hdr)
	}
	off := binary.LittleEndian.Uint32(ico[6+12 : 6+16])
	if !bytes.HasPrefix(ico[off:], []byte("\x89PNG")) {
		t.Fatal("ico entry is not PNG")
	}
}
