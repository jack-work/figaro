// Package doc is the pure, IO-free document model for a loosely append-only
// live log: blocks, the events that mutate them, and single-region deltas for
// compressing streamed text. It has no rendering or transport concerns, so it
// is trivially unit-testable and shared by the render and sync packages.
package doc

import "unicode/utf8"

// Delta is one contiguous edit to a string: replace the bytes [At, At+Del) with
// Ins. It is the unit of delta compression for streamed text — rather than
// resend a block's whole body on every update, only the changed span travels.
type Delta struct {
	At  int    `json:"at"`
	Del int    `json:"del"`
	Ins string `json:"ins"`
}

// Diff returns the minimal single-region Delta that turns old into new (longest
// common prefix + suffix, rune-aligned so a cut never lands mid-rune). ok is
// false when the strings are equal.
func Diff(old, new string) (Delta, bool) {
	if old == new {
		return Delta{}, false
	}
	oLen, nLen := len(old), len(new)

	a := 0 // longest common prefix (bytes)
	for a < oLen && a < nLen && old[a] == new[a] {
		a++
	}
	z := 0 // longest common suffix (bytes), not overlapping the prefix
	for z < oLen-a && z < nLen-a && old[oLen-1-z] == new[nLen-1-z] {
		z++
	}
	// Back both boundaries off to rune starts. The shared spans are identical in
	// both strings, so aligning on old aligns new; shrinking only widens the
	// changed middle, never re-splits it.
	for a > 0 && a < oLen && !utf8.RuneStart(old[a]) {
		a--
	}
	for z > 0 && !utf8.RuneStart(old[oLen-z]) {
		z--
	}
	return Delta{At: a, Del: oLen - a - z, Ins: new[a : nLen-z]}, true
}

// Apply applies d to s, clamping out-of-range offsets so a malformed delta
// degrades to a near-no-op rather than panicking (consumers can resync).
func Apply(s string, d Delta) string {
	at := clamp(d.At, 0, len(s))
	end := clamp(at+d.Del, at, len(s))
	return s[:at] + d.Ins + s[end:]
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
