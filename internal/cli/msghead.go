package cli

import (
	"github.com/jack-work/figaro/api/livedoc"
	"github.com/jack-work/figaro/internal/term"
)

// messageHeader returns the user-visible role label drawn above a
// message. It is the single source of truth for "who is speaking" in
// every view (inline, transcript, show). An empty string disables the
// header for a given role.
func messageHeader(role string) string {
	switch role {
	case livedoc.RoleInput:
		return term.Dim("> input")
	case livedoc.RoleOutput:
		return term.Dim("< figaro")
	default:
		return ""
	}
}

// dimSender styles a per-segment attribution for the inline view. It is the
// same dim register block timestamps and tool durations use, so a sender reads
// as metadata about the message rather than as part of it.
func dimSender(name string) string { return term.Dim(name) }
