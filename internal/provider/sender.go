package provider

import (
	"strings"

	"github.com/jack-work/figaro/api/handle"
	"github.com/jack-work/figaro/api/rpc"
)

func SenderReminder(sender string) string {
	bare, body, ok := figaroSender(sender)
	if !ok {
		return ""
	}
	short := bare[:len(bare)-len(body)] + body[:min(rpc.ShortIDLen, len(body))]
	name := "figaro " + short
	if words := handle.Words(bare, handle.MinWords); words != "" {
		name = "figaro " + words + " (" + short + ")"
	}
	return Reminder("sender", name)
}

func SenderProse(sender, text string) string {
	if _, _, ok := figaroSender(sender); !ok {
		return text
	}
	return NeutraliseReservedTags(text)
}

func figaroSender(sender string) (bare, body string, ok bool) {
	id, _ := strings.CutPrefix(sender, rpc.AriaLabelPrefix)
	_, bare = rpc.SplitNode(id)
	body = strings.TrimPrefix(bare, "@")
	return bare, body, rpc.IsHexID(bare) && len(body) >= rpc.OldIDLen
}
