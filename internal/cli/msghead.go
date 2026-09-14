package cli

import (
	"strings"

	"github.com/jack-work/figaro/api/livedoc"
	"github.com/jack-work/figaro/api/rpc"
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

func inputHeader(sender string) string {
	if sender == "" {
		return messageHeader(livedoc.RoleInput)
	}
	if id, ok := strings.CutPrefix(sender, rpc.AriaLabelPrefix); ok {
		sender = "figaro " + id
	}
	return term.Dim("> " + sender)
}
