// Package icon renders the tray icons at startup (no binary assets): a
// filled circle, accent-colored when connected and grey otherwise.
package icon

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
	"math"
)

var (
	Connected    = color.RGBA{0x5b, 0x5b, 0xd6, 0xff}
	Disconnected = color.RGBA{0x8a, 0x8a, 0x96, 0xff}
	Warning      = color.RGBA{0xe0, 0x8a, 0x00, 0xff}
)

// PNG draws a size x size anti-aliased ring with a center dot.
func PNG(size int, c color.RGBA) []byte {
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	cx, cy := float64(size)/2, float64(size)/2
	outer := float64(size)/2 - 0.5
	ring := outer * 0.30
	dot := outer * 0.36
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			d := math.Hypot(float64(x)+0.5-cx, float64(y)+0.5-cy)
			a := coverage(outer-d) * (1 - coverage(outer-ring-d)) // ring
			a = math.Max(a, coverage(dot-d))                      // center dot
			if a <= 0 {
				continue
			}
			img.SetNRGBA(x, y, color.NRGBA{c.R, c.G, c.B, uint8(float64(c.A) * a)})
		}
	}
	var b bytes.Buffer
	_ = png.Encode(&b, img)
	return b.Bytes()
}

func coverage(v float64) float64 { return math.Max(0, math.Min(1, v+0.5)) }

// ICO wraps PNG images in an .ico container (PNG-compressed entries are
// supported since Windows Vista).
func ICO(pngs map[int][]byte, sizes ...int) []byte {
	var b bytes.Buffer
	_ = binary.Write(&b, binary.LittleEndian, [3]uint16{0, 1, uint16(len(sizes))})
	offset := 6 + 16*len(sizes)
	for _, s := range sizes {
		dim := uint8(s)
		if s >= 256 {
			dim = 0
		}
		_ = binary.Write(&b, binary.LittleEndian, struct {
			W, H, Colors, Reserved uint8
			Planes, BPP            uint16
			Size, Offset           uint32
		}{dim, dim, 0, 0, 1, 32, uint32(len(pngs[s])), uint32(offset)})
		offset += len(pngs[s])
	}
	for _, s := range sizes {
		b.Write(pngs[s])
	}
	return b.Bytes()
}

// ForOS returns icon bytes in the format the tray expects on goos.
func ForOS(goos string, c color.RGBA) []byte {
	if goos == "windows" {
		return ICO(map[int][]byte{16: PNG(16, c), 32: PNG(32, c)}, 16, 32)
	}
	return PNG(64, c)
}
