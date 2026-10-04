package mister

import "fmt"

// Cores whose keyboard follows a Japanese (JIS) layout read the host's key
// positions, not its characters: on the MSX1 core with an FS-A1GT pack,
// typing US "(" shows ")" and US "*" shows "(".  jisFromUS gives, for each
// character wanted, the US character to send.  Every entry was checked on
// that machine by typing it in a REM line; so were the characters that
// pass through unchanged (letters, digits, space ! # $ % - , . / < > ? ;).
// '*', ']', '}', yen/backslash, '|' and '_' did not come out of any key
// tried (US '"' gives '"', US '\' gives yen, US '|' gives '|'), so they are
// refused rather than guessed.
var jisFromUS = map[rune]rune{
	'"':  '@', // shift+2
	'&':  '^', // shift+6
	'\'': '&', // shift+7
	'(':  '*', // shift+8
	')':  '(', // shift+9
	'=':  '_', // shift+-
	'^':  '=', // the key right of '-'
	'~':  '+',
	'@':  '[', // the key right of 'P'
	'`':  '{',
	'[':  ']',
	'{':  '}',
	'+':  ':', // shift+;
	':':  '\'',
}

// TranslateForJIS rewrites text so that typing the result on the emulated
// US keyboard produces text on a JIS-layout machine.
func TranslateForJIS(text string) (string, error) {
	out := make([]rune, 0, len(text))
	for _, r := range text {
		switch r {
		case '\\', '_', '|', '*', ']', '}':
			return "", fmt.Errorf("%q cannot be typed on this JIS-layout machine from a US keyboard (no key found for it)", r)
		}
		if u, ok := jisFromUS[r]; ok {
			out = append(out, u)
		} else {
			out = append(out, r)
		}
	}
	return string(out), nil
}
