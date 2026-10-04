package mister

import (
	"os"
	"strings"
	"testing"
)

// Tokens from the MSX1 core's CONF_STR (muhanpong/MSX1_MiSTer) that the parser
// used to misread.  Each case states what MiSTer main does with the token.

func TestParseTrigger_BracketBit(t *testing.T) {
	// "T[44],Pause" pulses status bit 44.  It used to parse as bit 0, which on
	// most cores is Reset.
	items := ParseConfStr("X;;T[44],Pause;t[45],Hidden pulse;T9,Legacy")
	want := []struct {
		typ string
		bit int
	}{{"trigger", 44}, {"trigger_hidden", 45}, {"trigger", 9}}
	for i, w := range want {
		it := items[i+1]
		if it.Type != w.typ || it.Bit != w.bit {
			t.Errorf("item %q: got type=%s bit=%d, want type=%s bit=%d", it.Raw, it.Type, it.Bit, w.typ, w.bit)
		}
	}
}

func TestParseMount_SCPrefix(t *testing.T) {
	// "SC<n>" is a mount row whose image is remembered across sessions
	// (the C flag).  It used to fall through to "label", so the row vanished
	// from the OSD row count.
	items := ParseConfStr("X;;SC4,VHD,Load SD card;SC1,NVR,SRAM;h1S5,DSK,Mount Drive A:")
	cases := []struct {
		idx   int
		label string
	}{{4, "Load SD card"}, {1, "SRAM"}, {5, "Mount Drive A:"}}
	for i, c := range cases {
		it := items[i+1]
		if it.Type != "mount" || it.Index != c.idx || it.Label != c.label {
			t.Errorf("item %q: got type=%s index=%d label=%q, want mount %d %q", it.Raw, it.Type, it.Index, it.Label, c.idx, c.label)
		}
	}
}

func TestParseHiddenSeparatorAndLabel(t *testing.T) {
	// "H6-" is a separator shown only when mask bit 6 is clear; "h9HA-,text"
	// is a non-selectable text row under two conditions.  Both used to come
	// back as standalone "hide" markers.
	items := ParseConfStr("X;;H6-;h9HA-,MIDI: MU-PACK (Slot B) active;hA-,MIDI: built-in")
	sep := items[1]
	if sep.Type != "separator" || len(sep.HideConditions) != 1 || sep.HideConditions[0].Bit != 6 || sep.HideConditions[0].Inverted {
		t.Errorf("H6-: got %+v", sep)
	}
	lbl := items[2]
	if lbl.Type != "separator" || lbl.Name != "MIDI: MU-PACK (Slot B) active" || len(lbl.HideConditions) != 2 {
		t.Fatalf("h9HA-: got %+v", lbl)
	}
	if c := lbl.HideConditions; !(c[0].Bit == 10 && !c[0].Inverted && c[1].Bit == 9 && c[1].Inverted) &&
		!(c[0].Bit == 9 && c[0].Inverted && c[1].Bit == 10 && !c[1].Inverted) {
		t.Errorf("h9HA-: conditions %+v", c)
	}
	if it := items[3]; it.Type != "separator" || it.Name != "MIDI: built-in" || len(it.HideConditions) != 1 || !it.HideConditions[0].Inverted {
		t.Errorf("hA-: got %+v", it)
	}
}

func TestStripCoreDateSuffix_LetterAndDescription(t *testing.T) {
	// RBF names like MSX1_20261004d_opl4regrd (date, build letter, short
	// description) used to keep the whole name, which the fuzzy lookup then
	// matched to "MSX" or even "C64".
	tests := []struct{ in, want string }{
		{"MSX1_20261004d_opl4regrd", "MSX1"},
		{"MSX1_20260927c_mupack", "MSX1"},
		{"MSX1_20261004a", "MSX1"},
		{"PC88_20250918", "PC88"},
		{"core_v2", "core_v2"},
		{"Foo_2026100_x", "Foo_2026100_x"},
	}
	for _, tt := range tests {
		if got := StripCoreDateSuffix(tt.in); got != tt.want {
			t.Errorf("StripCoreDateSuffix(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestRunningCoreNames_FromMiSTerMain(t *testing.T) {
	// MiSTer main writes the CONF_STR name to /tmp/RBFNAME and the name it
	// uses for <name>.CFG (the MRA set name on arcade cores) to /tmp/CORENAME.
	dir := t.TempDir()
	oldC, oldR := coreNameFilePath, rbfNameFilePath
	coreNameFilePath, rbfNameFilePath = dir+"/CORENAME", dir+"/RBFNAME"
	t.Cleanup(func() { coreNameFilePath, rbfNameFilePath = oldC, oldR })

	if c, r := runningNamesFromMain(); c != "" || r != "" {
		t.Fatalf("missing files: got %q %q, want empty", c, r)
	}
	os.WriteFile(coreNameFilePath, []byte("dkong\n"), 0644)
	os.WriteFile(rbfNameFilePath, []byte("DonkeyKong"), 0644)
	if c, r := runningNamesFromMain(); c != "dkong" || r != "DonkeyKong" {
		t.Errorf("got config=%q confstr=%q", c, r)
	}
}

func TestCoreStatusNames(t *testing.T) {
	s := CoreStatus{CoreName: "MSX1_20261004d_opl4regrd", ConfigName: "MSX1", ConfStrName: "MSX1"}
	if s.LookupName() != "MSX1" || s.CFGName() != "MSX1" {
		t.Errorf("with main's names: lookup=%q cfg=%q", s.LookupName(), s.CFGName())
	}
	// Without /tmp files: fall back to the RBF name with the suffix stripped
	s = CoreStatus{CoreName: "MSX1_20261004d_opl4regrd"}
	if s.LookupName() != "MSX1" || s.CFGName() != "MSX1" {
		t.Errorf("fallback: lookup=%q cfg=%q", s.LookupName(), s.CFGName())
	}
}

// H/h on a row refer to the core's OSD mask (UIO_GET_OSDMASK, menu.cpp),
// not to .CFG bits.  Hidden rows leave the cursor path; disabled rows stay
// on it.  Without the mask a position that depends on such rows is unknown.
const msx1NavSnippet = "MSX1;;" +
	"H7H2O[19:17],SLOT A,ROM,SCC;" + // shown when mask bits 7 and 2 are clear
	"H7h2O[19:17],SLOT A,ROM,FDC;" + // shown when bit 7 clear, bit 2 set
	"O[71],SLOT A sub-slots,Off,On;" +
	"-;O[43],Pause on OSD,No,Yes;" +
	"DDO[48],Debug Overlay,Off,On;" + // disabled rows keep their cursor stop
	"P1,Video settings;P1O[2:1],Aspect ratio,A,B;h2P1O[14:13],Video mode,AUTO,PAL;P1O[7:6],Scale,N,V;" +
	"-;T[0],Reset;R[0],Reset and close OSD"

func msx1NavOSD() *CoreOSD {
	return &CoreOSD{CoreName: "MSX1", ConfStrRaw: msx1NavSnippet}
}

func u32(v uint32) *uint32 { return &v }

func TestOSDPosition_UnknownMaskRefusesDependentRows(t *testing.T) {
	// Conditional rows both above and below: neither direction is certain.
	osd := &CoreOSD{CoreName: "X", ConfStrRaw: "X;;H1O1,Above,a,b;O2,Target,a,b;H2O3,Below,a,b"}
	_, err := FindOSDItemPositionMask(osd, "Target", nil)
	if err == nil || !strings.Contains(err.Error(), "osd_mask") || !strings.Contains(err.Error(), "H2O3") {
		t.Fatalf("want refusal naming the rows and asking for osd_mask, got %v", err)
	}
	// Only rows above uncertain: bottom-up works.
	if loc, err := FindOSDItemPositionMask(msx1NavOSD(), "Pause on OSD", nil); err != nil || !loc.UseBottomNav {
		t.Errorf("Pause on OSD: %+v %v; want bottom-up", loc, err)
	}
	if _, err := FindOSDItemPositionMask(msx1NavOSD(), "SLOT A", nil); err == nil {
		t.Error("ambiguous SLOT A accepted without a mask")
	}
}

func TestOSDPosition_WithMask(t *testing.T) {
	cases := []struct {
		mask   uint32
		target string
		pos    int
	}{
		{0x000, "SLOT A", 0},        // first SLOT A row
		{0x004, "SLOT A", 0},        // bit 2 set: the second SLOT A row is the visible one
		{0x000, "Pause on OSD", 2},  // SLOT A, sub-slots, Pause
		{0x080, "Pause on OSD", 1},  // bit 7 hides both SLOT A rows
		{0x000, "Debug Overlay", 3}, // disabled or not, still a stop
		{0x2000, "Debug Overlay", 3},
	}
	for _, c := range cases {
		loc, err := FindOSDItemPositionMask(msx1NavOSD(), c.target, u32(c.mask))
		if err != nil || loc.OnSubPage || loc.Position != c.pos {
			t.Errorf("mask %#x %q: got %+v, %v; want top-level %d", c.mask, c.target, loc, err, c.pos)
		}
	}
	if loc, err := FindOSDItemPositionMask(msx1NavOSD(), "SLOT A", u32(0x004)); err == nil && !strings.Contains(loc.Item.Raw, "FDC") {
		t.Errorf("bit 2 set should pick the FDC row, got %q", loc.Item.Raw)
	}
}

func TestOSDPosition_SubPageNeedsOnlyRowsOnItsPath(t *testing.T) {
	// Sub-pages are reached bottom-up, so unknown rows above the page entry
	// do not matter; a conditional row inside the page above the target does.
	loc, err := FindOSDItemPositionMask(msx1NavOSD(), "Aspect ratio", nil)
	if err != nil || !loc.OnSubPage || loc.Position != 0 || loc.BottomOffset != 2 {
		t.Fatalf("Aspect ratio without mask: %+v %v", loc, err)
	}
	if _, err := FindOSDItemPositionMask(msx1NavOSD(), "Scale", nil); err == nil {
		t.Error("Scale sits below a mask-dependent row in its page; want refusal")
	}
	for mask, pos := range map[uint32]int{0x000: 1, 0x004: 2} {
		loc, err := FindOSDItemPositionMask(msx1NavOSD(), "Scale", u32(mask))
		if err != nil || loc.Position != pos {
			t.Errorf("Scale mask %#x: %+v %v, want %d", mask, loc, err, pos)
		}
	}
}

func TestOSDPosition_TopLevelFallsBackToBottomUp(t *testing.T) {
	// Rows above Reset depend on the mask, rows below it do not: count up
	// from the bottom (the OSD wraps from the top row to Exit).
	loc, err := FindOSDItemPositionMask(msx1NavOSD(), "Reset", nil)
	if err != nil || loc.OnSubPage || !loc.UseBottomNav || loc.BottomOffset != 1 {
		t.Fatalf("Reset without mask: %+v %v; want bottom-up, 1 row below", loc, err)
	}
	// Known mask: plain top-down count.
	loc, err = FindOSDItemPositionMask(msx1NavOSD(), "Reset", u32(0))
	if err != nil || loc.UseBottomNav || loc.Position != 5 {
		t.Errorf("Reset with mask 0: %+v %v; want top-down 5", loc, err)
	}
}
