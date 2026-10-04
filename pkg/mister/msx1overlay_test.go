package mister

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"strings"
	"testing"
)

// drawOverlay paints a synthetic overlay panel at the given scale.
func drawOverlay(rows map[int]uint32, scale int) image.Image {
	w, h := 582*scale, 242*scale
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	bg := color.RGBA{65, 195, 227, 255}
	dim := color.RGBA{16, 16, 32, 255}
	lit := color.RGBA{255, 64, 255, 255}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, bg)
		}
	}
	for r := 0; r < overlayRows; r++ {
		bits, bw := 16, 4
		if r == 36 {
			bits, bw = 32, 2
		}
		for i := 0; i < bits; i++ {
			c := dim
			if rows[r]&(1<<uint(bits-1-i)) != 0 {
				c = lit
			}
			for dy := 0; dy < overlayRowHeight*scale; dy++ {
				for dx := 0; dx < bw*scale; dx++ {
					img.Set(i*bw*scale+dx, (1+overlayRowHeight*r)*scale+dy, c)
				}
			}
		}
	}
	for y := 0; y < (2+overlayRows*overlayRowHeight)*scale; y++ {
		for dx := 0; dx < scale; dx++ {
			img.Set((overlayPanelWidth-1)*scale+dx, y, color.RGBA{0, 0, 0, 255})
		}
	}
	return img
}

func TestDecodeMSX1Overlay_RoundTrip(t *testing.T) {
	want := map[int]uint32{9: 0x086E, 26: 0x8103, 28: 0x042A, 29: 0x4170, 36: 0xDEADBEEF, 37: 0xC000}
	for _, scale := range []int{1, 2} {
		var buf bytes.Buffer
		png.Encode(&buf, drawOverlay(want, scale))
		ov, err := DecodeMSX1Overlay(buf.Bytes())
		if err != nil {
			t.Fatalf("scale %d: %v", scale, err)
		}
		if ov.Scale != scale {
			t.Errorf("scale %d detected as %d", scale, ov.Scale)
		}
		for r, v := range want {
			if ov.Rows[r].Value != v {
				t.Errorf("scale %d row %d: got %X want %X", scale, r, ov.Rows[r].Value, v)
			}
		}
		if ov.Rows[37].BarPc != 12 { // 0xC000 = 2 bits = 8 px of 64
			t.Errorf("row 37 bar %d%%, want 12", ov.Rows[37].BarPc)
		}
		if ov.Rows[28].Hex != "042A" || ov.Rows[36].Hex != "DEADBEEF" {
			t.Errorf("hex: %s %s", ov.Rows[28].Hex, ov.Rows[36].Hex)
		}
	}
}

func TestDecodeMSX1Overlay_Absent(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 582, 242))
	for y := 0; y < 242; y++ {
		for x := 0; x < 582; x++ {
			img.Set(x, y, color.RGBA{32, 32, 227, 255})
		}
	}
	var buf bytes.Buffer
	png.Encode(&buf, img)
	if _, err := DecodeMSX1Overlay(buf.Bytes()); err == nil || !strings.Contains(err.Error(), "Off") {
		t.Fatalf("want 'overlay off' error, got %v", err)
	}
}
