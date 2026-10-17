package cli

import (
	"fmt"
	"strings"
)

// The one spelling of a chord, used in both directions: the help pit prints
// it, keys.toml is written in it, and parseKeySpec is its inverse. A second
// spelling would be a second definition, which is the defect the derived help
// column exists to remove.

// spell renders a chord the way a reader writes it.
func (c chord) spell() string {
	switch c.kind {
	case chordNav:
		return navName(c.nav)
	case chordCtrlLetter:
		// ^I IS Tab and ^M IS Enter: the CSI-u report carries the modifier,
		// but the key still has a name, and the name is what a reader writes.
		switch c.b {
		case 'i':
			return "Tab"
		case 'm':
			return "Enter"
		}
		return "^" + strings.ToUpper(string(c.b))
	case chordMeta:
		return "M-" + metaByteName(c.b)
	default:
		return byteName(c.b)
	}
}

func byteName(b byte) string {
	switch b {
	case 0x0d:
		return "Enter"
	case 0x09:
		return "Tab"
	case 0x1b:
		return "Esc"
	case 0x7f:
		return "Backspace"
	case ' ':
		return "Space"
	}
	if b < 0x20 {
		return "^" + string(rune('@'+b))
	}
	if b > 0x7e {
		return fmt.Sprintf("0x%02x", b)
	}
	return string(b)
}

func metaByteName(b byte) string {
	switch b {
	case 0x0d:
		return "Enter"
	case 0x7f:
		return "DEL"
	}
	return byteName(b)
}

// keySpec is what one written chord expands to. A control letter has TWO
// encodings a terminal may send (the raw byte, and the CSI-u report figaro
// asks for with \x1b[>1u), and a reader cannot be asked which one their
// terminal uses: writing ^N claims both.
type keySpec struct {
	text   string
	chords []chord
}

// weldedSpelling names the three chords that are another key's byte. Writing
// them is a mistake worth catching rather than honouring.
var weldedSpelling = map[string]string{
	"^M": "Enter",
	"^I": "Tab",
	"^[": "Esc",
}

func parseKeySpec(s string) (keySpec, error) {
	t := strings.TrimSpace(s)
	if t == "" {
		return keySpec{}, fmt.Errorf("empty chord")
	}
	if welded, bad := weldedSpelling[strings.ToUpper(t)]; bad {
		return keySpec{}, fmt.Errorf("%s is %s's byte: write %s", t, welded, welded)
	}
	if rest, ok := strings.CutPrefix(t, "csi:"); ok {
		if len(rest) != 2 || rest[0] != '^' {
			return keySpec{}, fmt.Errorf("csi: names a CSI-u control letter, as csi:^N")
		}
		b, err := controlByte(rest[1])
		if err != nil {
			return keySpec{}, err
		}
		letter, ok := ctrlLetterFor(b)
		if !ok {
			return keySpec{}, fmt.Errorf("%s has no CSI-u report", rest)
		}
		return keySpec{text: t, chords: []chord{ctrlChord(letter)}}, nil
	}
	if strings.HasPrefix(t, "0x") && len(t) == 4 {
		var b byte
		if _, err := fmt.Sscanf(t, "0x%02x", &b); err == nil {
			return keySpec{text: t, chords: []chord{byteChord(b)}}, nil
		}
	}
	if rest, ok := cutMeta(t); ok {
		b, err := parseMetaByte(rest)
		if err != nil {
			return keySpec{}, err
		}
		return keySpec{text: t, chords: []chord{metaChord(b)}}, nil
	}
	if n, ok := parseNavName(t); ok {
		return keySpec{text: t, chords: []chord{navChord(n)}}, nil
	}
	if b, ok := namedByte(t); ok {
		// Tab claims both encodings, because a CSI-u terminal reports it as
		// Ctrl+i. Enter and Esc do not: a terminal that can say which key was
		// pressed says Enter, and Esc is the sequence prefix.
		if b == 0x09 {
			return keySpec{text: t, chords: []chord{byteChord(b), ctrlChord('i')}}, nil
		}
		return keySpec{text: t, chords: []chord{byteChord(b)}}, nil
	}
	if len(t) == 2 && t[0] == '^' {
		b, err := controlByte(t[1])
		if err != nil {
			return keySpec{}, err
		}
		if letter, dual := ctrlLetterFor(b); dual {
			return keySpec{text: t, chords: []chord{byteChord(b), ctrlChord(letter)}}, nil
		}
		return keySpec{text: t, chords: []chord{byteChord(b)}}, nil
	}
	if len(t) == 1 && t[0] >= 0x20 && t[0] < 0x7f {
		return keySpec{text: t, chords: []chord{byteChord(t[0])}}, nil
	}
	return keySpec{}, fmt.Errorf("not a chord: %q", s)
}

func cutMeta(s string) (string, bool) {
	for _, p := range []string{"M-", "m-", "A-", "a-", "Alt-", "alt-"} {
		if rest, ok := strings.CutPrefix(s, p); ok && rest != "" {
			return rest, true
		}
	}
	return "", false
}

func parseMetaByte(s string) (byte, error) {
	switch strings.ToUpper(s) {
	case "ENTER":
		return 0x0d, nil
	case "DEL":
		return 0x7f, nil
	case "SPACE":
		return ' ', nil
	}
	if len(s) == 1 && s[0] >= 0x20 && s[0] < 0x7f {
		return metaFold(s[0]), nil
	}
	return 0, fmt.Errorf("not a key Meta can be held for: %q", s)
}

func namedByte(s string) (byte, bool) {
	switch strings.ToUpper(s) {
	case "ENTER", "RETURN", "CR":
		return 0x0d, true
	case "TAB":
		return 0x09, true
	case "ESC", "ESCAPE":
		return 0x1b, true
	case "BACKSPACE", "BS", "DEL":
		return 0x7f, true
	case "SPACE":
		return ' ', true
	}
	return 0, false
}

func parseNavName(s string) (navKey, bool) {
	switch strings.ToUpper(s) {
	case "UP":
		return navUp, true
	case "DOWN":
		return navDown, true
	case "PGUP", "PAGEUP":
		return navPageUp, true
	case "PGDN", "PAGEDOWN":
		return navPageDown, true
	case "HOME":
		return navHome, true
	case "END":
		return navEnd, true
	case "LEFT":
		return navLeft, true
	case "RIGHT":
		return navRight, true
	}
	return navNone, false
}

// controlByte is what ^<x> sends. The letters are ASCII's own answer; the
// punctuation controls reuse the table key_input.go already keeps.
func controlByte(x byte) (byte, error) {
	if x >= 'a' && x <= 'z' {
		x -= 0x20
	}
	if x >= '@' && x <= '_' {
		if b, ok := ctrlPunctuation[x]; ok {
			return b, nil
		}
		return x & 0x1f, nil
	}
	if b, ok := ctrlPunctuation[x]; ok {
		return b, nil
	}
	return 0, fmt.Errorf("not a control chord: ^%s", string(x))
}

// ctrlLetterFor reports the CSI-u chord a control byte is also reported as.
func ctrlLetterFor(b byte) (byte, bool) {
	if b >= 1 && b <= 26 {
		return byte('a' + b - 1), true
	}
	return 0, false
}

// ---------------------------------------------------------------------------
// Modes, named.

var modeNames = map[string]keyModeSet{
	"incipit":    inIncipit,
	"transcript": inTranscript,
	"search":     inSearchBox,
	"command":    inBox,
	"pit":        inPit,
	"list":       inList,
	"visual":     inVisual,
	"confirm":    inConfirm,
	"composer":   inComposer,
	"pager":      inPager,
	"all":        inAnyBox,
}

// modeName is the internal spelling, used by the oracles and by error text.
func modeName(m keyMode) string {
	switch m {
	case modeIncipit:
		return "incipit"
	case modeTranscript:
		return "transcript"
	case modeSearch:
		return "search"
	case modeBox:
		return "jump"
	case modePanel:
		return "panel"
	case modeVisual:
		return "visual"
	case modeConfirm:
		return "confirm"
	case modeComposer:
		return "composer"
	case modeList:
		return "list"
	}
	return "?"
}

// userModeName is the name a reader writes for one mode: `command` and `pit`
// read better than the internal `jump` and `panel`, and the help pit already
// calls them that.
func userModeName(m keyMode) string {
	switch m {
	case modeBox:
		return "command"
	case modePanel:
		return "pit"
	}
	return modeName(m)
}

func parseModes(s string) (keyModeSet, error) {
	if strings.TrimSpace(s) == "" {
		return inTranscript, nil
	}
	var set keyModeSet
	for _, part := range strings.Split(s, ",") {
		m, ok := modeNames[strings.ToLower(strings.TrimSpace(part))]
		if !ok {
			return 0, fmt.Errorf("unknown mode %q", strings.TrimSpace(part))
		}
		set |= m
	}
	return set, nil
}

// modeList spells a mode set the way a reader writes it.
func spellModes(set keyModeSet) string {
	if set == inAnyBox {
		return "all"
	}
	var names []string
	for m := keyMode(0); m < numKeyModes; m++ {
		if set&(1<<m) != 0 {
			names = append(names, userModeName(m))
		}
	}
	return strings.Join(names, ",")
}
