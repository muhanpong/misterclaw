package mister

import "fmt"

// Cores whose keyboard follows a Japanese (JIS) layout read the host's key
// positions, not its characters: on the MSX1 core with an FS-A1GT pack,
// typing US "(" shows ")" and US "*" shows "(".  jisFromUS gives, for each
// character wanted on a JIS machine, the US character whose key sits where
// the JIS key is.  Letters, digits, space, '!', '#', '$', '%', '-', ',',
// '.', '/', '<', '>', '?', ';' are in the same place.  Yen/backslash and
// '_' (the JIS "ro" key) have no US key and are refused.
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
	'*':  '"',
	']':  '\\',
	'}':  '|',
}

// TranslateForJIS rewrites text so that typing the result on the emulated
// US keyboard produces text on a JIS-layout machine.
func TranslateForJIS(text string) (string, error) {
	out := make([]rune, 0, len(text))
	for _, r := range text {
		switch r {
		case '\\', '_', '|':
			return "", fmt.Errorf("%q has no key on a US keyboard in a JIS layout", r)
		}
		if u, ok := jisFromUS[r]; ok {
			out = append(out, u)
		} else {
			out = append(out, r)
		}
	}
	return string(out), nil
}
