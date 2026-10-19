package cli

import (
	"strings"

	"github.com/jack-work/figaro/api/aria"
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

func inputHeader(seg aria.InquirySegment) string {
	if seg.Sender == "" {
		return messageHeader(livedoc.RoleInput)
	}
	return term.Dim("> " + senderLabel(seg.Sender, seg.Name))
}

func senderLabel(sender, name string) string {
	id, legacy := strings.CutPrefix(sender, rpc.AriaLabelPrefix)
	if name != "" && shownIDs == idsAsHandles {
		return "figaro " + name
	}
	if rpc.IsHexID(id) && len(strings.TrimPrefix(id, "@")) >= rpc.OldIDLen {
		return "figaro " + ariaName(id)
	}
	if legacy {
		return "figaro " + id
	}
	return sender
}
