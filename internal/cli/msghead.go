package cli

import (
	"strings"

	"github.com/jack-work/figaro/api/aria"
	"github.com/jack-work/figaro/api/livedoc"
	"github.com/jack-work/figaro/api/rpc"
	ldrender "github.com/jack-work/figaro/internal/livelog/render"
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

func inputHeader(seg aria.InquirySegment) (string, []ldrender.Spot) {
	if seg.Sender == "" {
		return messageHeader(livedoc.RoleInput), nil
	}
	head := "> " + senderLabel(seg.Sender, seg.Name)
	return term.Dim(head), otherSpot(ldrender.Width(head), senderOther(seg.Sender, seg.Name))
}

func steeringHead(n livedoc.Node) (string, []ldrender.Spot) {
	head := term.Dim("↳ input")
	if n.Sender == "" {
		return head, nil
	}
	label := "· " + senderLabel(n.Sender, n.SenderName)
	head += " " + term.Dim(label)
	return head, otherSpot(ldrender.Width("↳ input "+label), senderOther(n.Sender, n.SenderName))
}

func headMore(n livedoc.Node) []ldrender.Spot {
	if n.Type != livedoc.NodeSteering {
		return nil
	}
	_, spots := steeringHead(n)
	return spots
}

func senderOther(sender, name string) string {
	id, _ := strings.CutPrefix(sender, rpc.AriaLabelPrefix)
	if !rpc.IsHexID(id) || len(strings.TrimPrefix(id, "@")) < rpc.OldIDLen {
		return ""
	}
	if shownIDs == idsAsShortHex && name != "" {
		return name
	}
	return otherName(id)
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
