package cli

import "github.com/jack-work/figaro/internal/term"

// messageHeader returns the user-visible role label drawn above a
// message. It is the single source of truth for "who is speaking" in
// every view (inline, transcript, show). An empty string disables the
// header for a given role.
//
// Convention:
//   "user"      → "❯ you"     (bright cyan — your voice)
//   "assistant" → "‹ figaro"  (dim — the agent's voice)
//   anything else (e.g. "system", "tool") → no header
//
// A steering interjection inside an assistant turn is a NODE
// (livedoc.NodeSteering), not a message role, and carries its own
// inline "↳ you" marker; this helper does not touch it.
func messageHeader(role string) string {
	switch role {
	case "user":
		return term.Cyan("❯ you")
	case "assistant":
		return term.Dim("‹ figaro")
	default:
		return ""
	}
}
