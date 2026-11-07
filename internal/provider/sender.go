package provider

import (
	"strings"

	"github.com/jack-work/figaro/api/handle"
	"github.com/jack-work/figaro/api/rpc"
)

func SenderReminder(sender string) string {
	id, _ := strings.CutPrefix(sender, rpc.AriaLabelPrefix)
	_, bare := rpc.SplitNode(id)
	body := strings.TrimPrefix(bare, "@")
	if !rpc.IsHexID(bare) || len(body) < rpc.OldIDLen {
		return ""
	}
	short := bare[:len(bare)-len(body)] + body[:min(rpc.ShortIDLen, len(body))]
	name := "figaro " + short
	if words := handle.Words(bare, handle.MinWords); words != "" {
		name = "figaro " + words + " (" + short + ")"
	}
	return "<system-reminder name=\"sender\">\n" + name + "\n</system-reminder>"
}
