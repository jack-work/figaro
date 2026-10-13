package cli

import (
	"strings"

	"github.com/jack-work/figaro/api/livedoc"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/term"
)

// messageHeader is the one source of the speaker label for every view; "" disables it.
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
