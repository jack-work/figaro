// Package livedoc is the core of the live-render protocol: a document is one
// UTF-8 markdown string that mutates only by single-region splices.
package livedoc

import "unicode/utf8"

// SpinnerSentinel is the PUA rune the producer writes for a running spinner;
// the renderer substitutes a frame locally, so animation never hits the wire.
const SpinnerSentinel = '\ue000'

var SpinnerFrames = []rune("⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏")

// Delta replaces bytes [At, At+Del) with Ins; both bounds are rune-aligned.
type Delta struct {
	At  int    `json:"at"`
	Del int    `json:"del"`
	Ins string `json:"ins"`
}

// Apply clamps out-of-range offsets; consumers resync via snapshot.
func Apply(blob string, d Delta) string {
	at := d.At
	if at < 0 {
		at = 0
	}
	if at > len(blob) {
		at = len(blob)
	}
	end := at + d.Del
	if end < at {
		end = at
	}
	if end > len(blob) {
		end = len(blob)
	}
	return blob[:at] + d.Ins + blob[end:]
}

// Diff is the minimal rune-aligned single-region delta; ok is false when
// old == new.
func Diff(old, new string) (Delta, bool) {
	if old == new {
		return Delta{}, false
	}
	oLen, nLen := len(old), len(new)

	a := 0
	for a < oLen && a < nLen && old[a] == new[a] {
		a++
	}
	z := 0
	for z < oLen-a && z < nLen-a && old[oLen-1-z] == new[nLen-1-z] {
		z++
	}

	// Both boundaries back off to a rune start; the common regions are
	// identical in old and new, so aligning on old aligns new.
	for a > 0 && a < oLen && !utf8.RuneStart(old[a]) {
		a--
	}
	for z > 0 && !utf8.RuneStart(old[oLen-z]) {
		z--
	}

	return Delta{At: a, Del: oLen - a - z, Ins: new[a : nLen-z]}, true
}
