package mister

import (
	"bytes"
	"fmt"
	"image"
	"image/png"
)

// The MSX1 core's debug overlay (rtl/debug_overlay.sv, Debug Overlay = On):
// a 66-pixel-wide panel on the left, 39 rows of 6 lines below a 1-line top
// border, each row a 16-bit value drawn 4 pixels per bit, MSB on the left,
// set bits bright.  Row 36 packs 32 bits at 2 pixels per bit.  A one-pixel
// black column closes the panel on the right.  Screenshots from MiSTer
// main hold the core's own pixels (no scaling seen on this core), but the
// scale is measured from that column rather than assumed.
//
// What each row means depends on the core build: rtl/msx.sv reassigns the
// dbg_* signals from time to time (2026-09-01 turned six rows into an A8
// transaction ring).  The labels below follow the msx1-debug-overlay skill
// as of the 2ce73fc build; check msx.sv before quoting a row.

const (
	overlayPanelWidth = 66
	overlayRows       = 39
	overlayRowHeight  = 6
)

var msx1OverlayLabels = [overlayRows]string{
	"ROM base set (green) / none (red)", "PCM valid", "PCM level (bar)", "NEW2", "per-slot voice map",
	"dead voices (bar)", "freeze detector cells", "PC at green latch", "vector target PC", "LIVE PC",
	"IM+I at last INTA", "WATCH PC", "WATCH {data, count}", "tx0 PC (A8 ring)", "tx0 {value, write}",
	"tx1 PC", "tx1 {value, write}", "tx2 PC", "tx2 {value, write}", "SP at first spin increment",
	"RST 38 spin count", "PPI port A {at trap, now}", "PC of last OUT (A8)", "{A8 value, A8 write count}",
	"{last IN A,(A8), zero-read count}", "RAMPAGE ORIGIN (sticky)", "{ms, rv, 8255 ctrl, reset count}",
	"{AB mode-set value, count}", "PC of last AB mode set (normal 042A)", "WHO JUMPED TO 0000 (sticky)",
	"probe_r2", "probe_r23", "probe_r0", "probe_r1", "probe_r9", "probe_r19", "probe frame (32-bit)",
	"P5 WAIT ratio (bar, 64px = 100%)", "P5 LATCH HIT ratio (bar, 64px = 100%)",
}

// OverlayRow is one decoded row.
type OverlayRow struct {
	Index int    `json:"row"`
	Label string `json:"label"`
	Value uint32 `json:"value"`   // 16 bits (32 for row 36)
	Hex   string `json:"hex"`     // Value as hex
	LitPx int    `json:"lit_px"`  // bright pixels in the bar area (bars: length)
	BarPc int    `json:"bar_pct"` // LitPx as a percentage of the 64-pixel bar
}

// MSX1Overlay is the decoded panel.
type MSX1Overlay struct {
	Scale int          `json:"scale"`
	Rows  []OverlayRow `json:"rows"`
	Note  string       `json:"note"`
}

func bright(c interface{ RGBA() (r, g, b, a uint32) }) bool {
	r, g, b, _ := c.RGBA()
	m := r
	if g > m {
		m = g
	}
	if b > m {
		m = b
	}
	return m >= 0x8000
}

func black(c interface{ RGBA() (r, g, b, a uint32) }) bool {
	r, g, b, _ := c.RGBA()
	return r < 0x1000 && g < 0x1000 && b < 0x1000
}

// DecodeMSX1Overlay reads the overlay out of a screenshot (PNG).
func DecodeMSX1Overlay(pngData []byte) (*MSX1Overlay, error) {
	img, err := png.Decode(bytes.NewReader(pngData))
	if err != nil {
		return nil, fmt.Errorf("screenshot: %w", err)
	}
	return decodeMSX1OverlayImage(img)
}

func decodeMSX1OverlayImage(img image.Image) (*MSX1Overlay, error) {
	b := img.Bounds()
	// Find the black closing column: the same x at the middle of several rows.
	scale := 0
	for s := 1; s <= 4 && scale == 0; s++ {
		x := b.Min.X + (overlayPanelWidth-1)*s
		if x >= b.Max.X || b.Min.Y+(1+overlayRows*overlayRowHeight)*s > b.Max.Y {
			break
		}
		ok := 0
		for _, r := range []int{7, 9, 20, 28, 38} {
			y := b.Min.Y + (1+overlayRowHeight*r+overlayRowHeight/2)*s
			if black(img.At(x, y)) && !black(img.At(x+s, y)) {
				ok++
			}
		}
		if ok >= 4 {
			scale = s
		}
	}
	if scale == 0 {
		return nil, fmt.Errorf("no MSX1 debug overlay in the screenshot (Debug Overlay is probably Off)")
	}
	ov := &MSX1Overlay{Scale: scale, Note: "row meanings depend on the core build (rtl/msx.sv dbg_* assignments); see the msx1-debug-overlay skill"}
	for r := 0; r < overlayRows; r++ {
		y := b.Min.Y + (1+overlayRowHeight*r+overlayRowHeight/2)*scale
		bits, bw := 16, 4
		if r == 36 {
			bits, bw = 32, 2
		}
		var v uint32
		for i := 0; i < bits; i++ {
			x := b.Min.X + (bw*i+bw/2)*scale
			if bright(img.At(x, y)) {
				v |= 1 << uint(bits-1-i)
			}
		}
		lit := 0
		for x := 0; x < 64; x++ {
			if bright(img.At(b.Min.X+x*scale+scale/2, y)) {
				lit++
			}
		}
		width := 4
		if bits == 32 {
			width = 8
		}
		ov.Rows = append(ov.Rows, OverlayRow{
			Index: r, Label: msx1OverlayLabels[r], Value: v,
			Hex: fmt.Sprintf("%0*X", width, v), LitPx: lit, BarPc: lit * 100 / 64,
		})
	}
	return ov, nil
}
