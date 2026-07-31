package cli

import (
	"github.com/jack-work/figaro/internal/livedoc"
	"github.com/jack-work/figaro/internal/term"
)

// messageHeader returns the user-visible role label drawn above a
// message. It is the single source of truth for "who is speaking" in
// every view (inline, transcript, show). An empty string disables the
// header for a given role.
//
// Convention (all dim so the block visually coheres with the rest of
// the app — the transcript rule, the assistant bookend, and the
// steering gutter all live in the same tonal register):
//
//	RoleInput  → "> input"   (dim — the incoming voice)
//	RoleOutput → "< figaro"  (dim — the agent's voice)
//	anything else (e.g. "system", "tool") → no header
//
// The asymmetry is deliberate. "you" was a LIE the moment a message arrived
// from a subagent rather than a person — which is why the roles became
// input/output at all. "figaro" is not a lie: it is genuinely the agent's
// name, so the output side keeps it rather than being flattened to "output"
// for the sake of a symmetry nobody asked for.
//
// A steering interjection inside a turn is a NODE
// (livedoc.NodeSteering), not a message role, and carries its own
// inline "↳ input" marker; this helper does not touch it.
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
//
// Package-level because `term` is shadowed by a local terminal variable at the
// call site — a method value there would silently resolve to the wrong thing.
func dimSender(name string) string { return term.Dim(name) }
