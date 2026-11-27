package provider

import (
	"strings"

	"github.com/jack-work/figaro/api/handle"
	"github.com/jack-work/figaro/api/rpc"
)

func SenderReminder(sender string) string {
	name := SenderName(sender)
	if name == "" {
		return ""
	}
	return Reminder("sender", name)
}

func SenderName(sender string) string {
	bare, body, ok := figaroSender(sender)
	if !ok {
		return ""
	}
	short := bare[:len(bare)-len(body)] + body[:min(rpc.ShortIDLen, len(body))]
	if words := handle.Words(bare, handle.MinWords); words != "" {
		return "figaro " + words + " (" + short + ")"
	}
	return "figaro " + short
}

func Prose(text string) string { return NeutraliseReservedTags(text) }

func figaroSender(sender string) (bare, body string, ok bool) {
	id, _ := strings.CutPrefix(sender, rpc.AriaLabelPrefix)
	_, bare = rpc.SplitNode(id)
	body = strings.TrimPrefix(bare, "@")
	return bare, body, rpc.IsHexID(bare) && len(body) >= rpc.OldIDLen
}
