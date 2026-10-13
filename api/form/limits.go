package form

import (
	"encoding/json"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Delta limits: how much of a form change an aria is shown.
const (
	// DeltaKeyBytesKey caps one property's rendered value.
	DeltaKeyBytesKey = "system.delta_key_bytes"
	// DeltaBytesKey caps the whole rendered block for one form at one stamp,
	// before the elision tail.
	DeltaBytesKey = "system.delta_bytes"
)

const (
	DefaultDeltaKeyBytes = 2048
	DefaultDeltaBytes    = 8192
)

type DeltaLimits struct {
	KeyBytes   int
	TotalBytes int
}

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
		return fallback
	}
	if n == 0 {
		return fallback
	}
	return n
}

// TruncateUTF8 cuts s to at most n bytes without splitting a rune (providers
// reject invalid UTF-8) and marks the cut. n <= 0 is unbounded.
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

// EllipsisNote is the tail a bounded render appends, naming the verb that
// shows the rest.
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
