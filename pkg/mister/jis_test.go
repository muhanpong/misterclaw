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
	for _, bad := range []string{"A_B", "2*3", "A(1]", "{}"} {
		if _, err := TranslateForJIS(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}
