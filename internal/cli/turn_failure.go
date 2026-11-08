package cli

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/jack-work/figaro/api/form"
)

type turnFailure struct {
	turn   uint64
	reason string
}

func failureOf(snap form.Snapshot) (turnFailure, bool) {
	var f turnFailure
	raw, ok := snap.Get(form.FailureReasonKey)
	if !ok || json.Unmarshal(raw, &f.reason) != nil || f.reason == "" {
		return turnFailure{}, false
	}
	f.reason = pitText(strings.Join(strings.Fields(f.reason), " "))
	if raw, ok := snap.Get(form.FailureTurnKey); ok {
		_ = json.Unmarshal(raw, &f.turn)
	}
	return f, true
}

func (f turnFailure) line() string {
	if f.turn == 0 {
		return "failed: " + f.reason
	}
	return fmt.Sprintf("turn %d failed: %s", f.turn, f.reason)
}
