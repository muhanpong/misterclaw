package mister

import "testing"

func TestTranslateForJIS(t *testing.T) {
	// Seen on the MSX1 core with an FS-A1GT pack: typing US "*196(" gave
	// "(196)", US "^" gave "&", US "=" gave "^", US ":" gave "+".
	got, err := TranslateForJIS(`PRINT INP(196)`)
	if err != nil || got != "PRINT INP*196(" {
		t.Errorf("got %q %v", got, err)
	}
	got, _ = TranslateForJIS(`&^+`)
	if got != "^=:" {
		t.Errorf("got %q", got)
	}
	if _, err := TranslateForJIS(`A_B`); err == nil {
		t.Error("'_' accepted")
	}
}
