package mister

import (
	"os"
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
