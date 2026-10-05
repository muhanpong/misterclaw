package mister

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// cfgWith builds a 16-byte .CFG with the given status fields set.
func cfgWith(fields map[[2]int]int) []byte {
	b := make([]byte, 16)
	for r, v := range fields {
		SetBitRange(b, r[0], r[1], v)
	}
	return b
}

// packRecord returns one 16-byte pack header (createMSXpack.py layout).
func packRecord(cfgType, slotSubslot, b4, sizeBlocks, b7, mode byte) []byte {
	h := make([]byte, 16)
	copy(h, "MSX")
	h[3] = cfgType<<4 | slotSubslot
	h[4] = b4
	h[6] = sizeBlocks
	h[7] = b7
	h[9] = mode
	return h
}

// writePack writes a minimal pack: a 16 KB BIOS ROM in slot 0-0 whose byte
// 002Dh is ver, optionally an FDC block and a MIDI device record.
func writePack(t *testing.T, ver byte, fdc, midi bool) string {
	var d []byte
	d = append(d, packRecord(4, 0, 1, 1, 0xFF, 0x03)...) // slot 0-0, ROM, 1 block, page 0 mapped
	rom := make([]byte, 16384)
	rom[0x2D] = ver
	d = append(d, rom...)
	if fdc {
		d = append(d, packRecord(4, 0x0E, 3, 1, 0xFF, 0x0C)...) // slot 3-2, FDC
		d = append(d, make([]byte, 16384)...)
	}
	d = append(d, packRecord(4, 0x0C, 2, 4, 0xFF, 0xAA)...) // RAM: no payload
	if midi {
		d = append(d, packRecord(7, 0, 0, 0, 5, 0)...) // DEVICE MIDI (id 5), no ROM
	}
	d = append(d, packRecord(6, 0, 0x10, 0, 0, 0)...) // CONFIG
	p := filepath.Join(t.TempDir(), "test.MSX")
	if err := os.WriteFile(p, d, 0644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestParseMSX1Pack(t *testing.T) {
	info, err := ParseMSX1Pack(writePack(t, 3, true, true))
	if err != nil {
		t.Fatal(err)
	}
	if info.Ver != 3 || !info.UseFDC || !info.MIDI {
		t.Errorf("GT-like pack: %+v", info)
	}
	info, err = ParseMSX1Pack(writePack(t, 1, false, false))
	if err != nil || info.Ver != 1 || info.UseFDC || info.MIDI {
		t.Errorf("plain MSX2 pack: %+v %v", info, err)
	}
	bad := filepath.Join(t.TempDir(), "bad.MSX")
	os.WriteFile(bad, []byte("not a pack"), 0644)
	if _, err := ParseMSX1Pack(bad); err == nil {
		t.Error("garbage accepted")
	}
}

func bitOf(m OSDMask, b int) (val, known bool) {
	return m.Value&(1<<uint(b)) != 0, m.Known&(1<<uint(b)) != 0
}

func TestInferMSX1Mask_CFGBits(t *testing.T) {
	// Slot A = ROM (0), mapper auto (0), not expanded; slot B = Empty (4),
	// expanded with FM-PAC + SCC+; tape from ADC.
	cfg := cfgWith(map[[2]int]int{{8, 8}: 1, {29, 31}: 4, {72, 72}: 1, {85, 87}: 4, {88, 90}: 3})
	m, _ := InferMSX1Mask(cfg, nil)
	want := map[int]bool{0: true, 3: false, 4: true, 5: true, 7: false, 8: true, 11: false, 12: true, 14: false}
	for b, v := range want {
		got, known := bitOf(m, b)
		if !known || got != v {
			t.Errorf("bit %d: got %v known=%v, want %v", b, got, known, v)
		}
	}
	for _, b := range []int{1, 2, 9, 10, 13, 15} {
		if _, known := bitOf(m, b); known && b != 9 {
			t.Errorf("bit %d should be unknown without the pack", b)
		}
	}
	// Slot B sub-slot 0 = FM-PAC: 8 KB SRAM, so bit 6 is known clear
	// (checked on hardware 2026-10-05: the SRAM rows were there).
	if v, known := bitOf(m, 6); !known || v {
		t.Errorf("bit 6 with an FM-PAC: got %v known=%v, want clear", v, known)
	}
	// No SRAM device anywhere: bit 6 stays unknown.
	m, _ = InferMSX1Mask(cfgWith(nil), nil)
	if _, known := bitOf(m, 6); known {
		t.Error("bit 6 known without an SRAM device")
	}
}

func TestInferMSX1Mask_SubslotDedupe(t *testing.T) {
	// Slot A expanded: sub 0 = SCC (consumes the file), sub 1 = ROM -> None.
	// So a file is present (bit 3 clear) but no ROM (bit 11 set, bit 5 set).
	cfg := cfgWith(map[[2]int]int{{71, 71}: 1, {73, 75}: 2, {76, 78}: 1})
	m, _ := InferMSX1Mask(cfg, nil)
	for b, v := range map[int]bool{3: false, 11: true, 5: true, 7: true} {
		if got, known := bitOf(m, b); !known || got != v {
			t.Errorf("bit %d: got %v known=%v, want %v", b, got, known, v)
		}
	}
}

func TestInferMSX1Mask_ASCII16XNeedsROMSize(t *testing.T) {
	// Mapper entry 3 (ASCII16X) hides SRAM size only for a ROM > 4 MB.
	cfg := cfgWith(map[[2]int]int{{20, 23}: 3})
	m, _ := InferMSX1Mask(cfg, nil)
	if _, known := bitOf(m, 5); known {
		t.Error("bit 5 must be unknown for ASCII16X without the ROM size")
	}
}

func TestInferMSX1Mask_WithPack(t *testing.T) {
	cfg := cfgWith(map[[2]int]int{{29, 31}: 5}) // slot B = MU-PACK, not expanded
	info := &MSX1PackInfo{Ver: 3, UseFDC: true, MIDI: false}
	m, _ := InferMSX1Mask(cfg, info)
	for b, v := range map[int]bool{1: true, 2: true, 9: true, 10: false, 13: true} {
		if got, known := bitOf(m, b); !known || got != v {
			t.Errorf("bit %d: got %v known=%v, want %v", b, got, known, v)
		}
	}
	// Slot A = FDC (6) on a pack without FDC: fdc_enabled from the slot.
	cfg = cfgWith(map[[2]int]int{{17, 19}: 6})
	m, _ = InferMSX1Mask(cfg, &MSX1PackInfo{Ver: 1})
	if got, known := bitOf(m, 1); !known || !got {
		t.Error("slot A FDC should set bit 1")
	}
}

func TestOSDPosition_PartialMask(t *testing.T) {
	// Only bit 7 known (clear): the two SLOT A rows still depend on bit 2.
	m := OSDMask{Value: 0, Known: 1 << 7}
	if _, err := FindOSDItemPositionKnown(msx1NavOSD(), "Pause on OSD", m); err != nil && !strings.Contains(err.Error(), "bit 2") {
		t.Errorf("error should name the unknown bit: %v", err)
	}
	// Bit 7 known set hides both SLOT A rows whatever bit 2 is.
	m = OSDMask{Value: 1 << 7, Known: 1 << 7}
	loc, err := FindOSDItemPositionKnown(msx1NavOSD(), "Pause on OSD", m)
	if err != nil || loc.UseBottomNav || loc.Position != 1 {
		t.Errorf("bit 7 set: %+v %v, want top-down 1", loc, err)
	}
}

func TestCheckMSX1Launch(t *testing.T) {
	dir := t.TempDir()
	oldC, oldP := MSX1CFGPath, MSX1PackRecordPath
	MSX1CFGPath, MSX1PackRecordPath = filepath.Join(dir, "MSX1.CFG"), filepath.Join(dir, "MSX1.f1")
	t.Cleanup(func() { MSX1CFGPath, MSX1PackRecordPath = oldC, oldP })

	// Slot A = SCC (1): no slot A ROM row -> refuse .rom
	os.WriteFile(MSX1CFGPath, cfgWith(map[[2]int]int{{17, 19}: 1}), 0644)
	if err := checkMSX1Launch("/x/game.rom"); err == nil || !strings.Contains(err.Error(), "SLOT A") {
		t.Errorf("slot A SCC: want refusal, got %v", err)
	}
	// Slot A expanded -> refuse .rom
	os.WriteFile(MSX1CFGPath, cfgWith(map[[2]int]int{{71, 71}: 1}), 0644)
	if err := checkMSX1Launch("/x/game.rom"); err == nil || !strings.Contains(err.Error(), "expanded") {
		t.Errorf("expanded: want refusal, got %v", err)
	}
	// Slot A = ROM -> fine; .dsk with a pack without FDC -> refuse
	os.WriteFile(MSX1CFGPath, cfgWith(nil), 0644)
	if err := checkMSX1Launch("/x/game.rom"); err != nil {
		t.Errorf("slot A ROM: %v", err)
	}
	pack := writePack(t, 1, false, false)
	os.WriteFile(MSX1PackRecordPath, []byte(pack), 0644)
	if err := checkMSX1Launch("/x/disk.dsk"); err == nil || !strings.Contains(err.Error(), "floppy") {
		t.Errorf("no FDC: want refusal, got %v", err)
	}
	os.WriteFile(MSX1PackRecordPath, []byte(writePack(t, 1, true, false)), 0644)
	if err := checkMSX1Launch("/x/disk.dsk"); err != nil {
		t.Errorf("FDC pack: %v", err)
	}
}
