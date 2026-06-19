// Package livedoc is the canonical core of the live-render protocol: a
// document is one UTF-8 markdown string ("the blob") that mutates only
// by single-region splices ("deltas"). No rendering, no I/O.
//
// A Delta replaces the half-open byte range [At, At+Del) of a blob with
// Ins. Diff derives the minimal single-region delta between two blobs
// (longest common prefix + suffix, differing middle). Both the producer
// and every consumer share this file, so a blob evolves identically on
// all sides: Apply(old, Diff(old, new)) == new.
//
// Invariant: every delta lands on UTF-8 rune boundaries. The byte-level
// prefix/suffix scan can fall mid-glyph (the blob carries braille
// spinners, ✓/✗); Diff backs each boundary off to the nearest rune
// start so Ins/Del never split a rune and the blob stays valid UTF-8.
package livedoc

import "unicode/utf8"

// Delta is a single-region splice in byte offsets: replace [At, At+Del)
// with Ins. At and At+Del are always rune-aligned.
type Delta struct {
	At  int    `json:"at"`
	Del int    `json:"del"`
	Ins string `json:"ins"`
}

// IsEmpty reports whether d changes nothing.
func (d Delta) IsEmpty() bool { return d.Del == 0 && d.Ins == "" }

// Apply returns blob with d applied. Out-of-range offsets are clamped
// rather than panicking, so a malformed delta degrades to a no-op-ish
// splice instead of crashing a consumer (faults resync via snapshot).
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

// Diff returns the minimal single-region delta transforming old into
// new, and ok=false when they are already equal. The delta is
// rune-aligned: Apply(old, Diff(old,new)) == new, and new stays valid
// UTF-8 whenever old is.
func Diff(old, new string) (Delta, bool) {
	if old == new {
		return Delta{}, false
	}
	oLen, nLen := len(old), len(new)

	// Longest common prefix (bytes).
	a := 0
	for a < oLen && a < nLen && old[a] == new[a] {
		a++
	}
	// Longest common suffix (bytes), not overlapping the prefix.
	z := 0
	for z < oLen-a && z < nLen-a && old[oLen-1-z] == new[nLen-1-z] {
		z++
	}

	// Back the prefix boundary off to a rune start. The rune structure
	// of [0,a) is identical in old and new, so aligning on old aligns
	// new too.
	for a > 0 && a < oLen && !utf8.RuneStart(old[a]) {
		a--
	}
	// Back the suffix boundary off to a rune start. The common suffix is
	// identical in both, so aligning on old aligns new too. Shrinking z
	// only lengthens the changed middle, never re-splits.
	for z > 0 && !utf8.RuneStart(old[oLen-z]) {
		z--
	}

	return Delta{At: a, Del: oLen - a - z, Ins: new[a : nLen-z]}, true
}
