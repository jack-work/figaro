package form

import (
	"encoding/json"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Delta limits: how much of a form change an aria is shown.
const (
	// DeltaKeyBytesKey caps ONE property's rendered value.
	DeltaKeyBytesKey = "system.delta_key_bytes"
	// DeltaBytesKey caps the whole rendered block for one form at one
	// stamp: the sum of the values shown, before the elision tail.
	DeltaBytesKey = "system.delta_bytes"
)

const (
	// DefaultDeltaKeyBytes holds a generous prose value (a mantra, a
	// brief, a paragraph of instruction) whole, and cuts a blob.
	DefaultDeltaKeyBytes = 2048
	// DefaultDeltaBytes holds a working board's worth of change: a role
	// carrying several keys of instruction folds inside it; a form used
	// as a scratch document does not.
	DefaultDeltaBytes = 8192
)

// DeltaLimits is the pair, resolved.
type DeltaLimits struct {
	KeyBytes   int
	TotalBytes int
}

// ReadDeltaLimits resolves the limits from a board, falling back to the
// built-in defaults. Called once per rendered message on the study
// path, beside ReadStudyIncantation: two scalar lookups.
func ReadDeltaLimits(snap Snapshot) DeltaLimits {
	return DeltaLimits{
		KeyBytes:   readIntKey(snap, DeltaKeyBytesKey, DefaultDeltaKeyBytes),
		TotalBytes: readIntKey(snap, DeltaBytesKey, DefaultDeltaBytes),
	}
}

func readIntKey(snap Snapshot, key string, fallback int) int {
	raw, ok := snap.Get(key)
	if !ok {
		return fallback
	}
	var n int
	if err := json.Unmarshal(raw, &n); err != nil {
		// A malformed limit is not worth failing a turn over, and
		// silently unbounding it would be worse than the default.
		return fallback
	}
	if n == 0 {
		return fallback
	}
	return n
}

// TruncateUTF8 cuts s to at most n bytes WITHOUT splitting a rune -- a
// provider rejects invalid UTF-8 outright, so a byte-exact cut is a 400
// waiting to happen -- and marks the cut so the reader knows the value
// continues. n <= 0 is unbounded. Reports whether it cut.
func TruncateUTF8(s string, n int) (string, bool) {
	if n <= 0 || len(s) <= n {
		return s, false
	}
	cut := n
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	if cut == 0 {
		return "", true
	}
	return s[:cut] + "…", true
}

// EllipsisNote is the tail a bounded render appends, naming the verb
// that shows the rest. Executable advice on purpose: a reader told
// "there is more" and not told where is worse off than one told
// nothing.
func EllipsisNote(formID string, more int) string {
	if more <= 0 {
		return ""
	}
	noun := "deltas"
	if more == 1 {
		noun = "delta"
	}
	id := formID
	if id == "" {
		id = "<aria-id | studied-form-id>"
	}
	return strings.Join([]string{
		"… and ", strconv.Itoa(more), " more ", noun,
		". Call `fig form ", id, " show` to see their current value.",
	}, "")
}
