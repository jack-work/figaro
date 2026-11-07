package provider

import (
	"strings"

	"github.com/jack-work/figaro/api/handle"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/api/rpc"
)

func PromptText(c message.Content) string {
	line := SenderLine(c.Sender)
	if line == "" || c.Text == "" {
		return c.Text
	}
	return line + "\n\n" + c.Text
}

func SenderLine(sender string) string {
	id, _ := strings.CutPrefix(sender, rpc.AriaLabelPrefix)
	_, bare := rpc.SplitNode(id)
	if !rpc.IsHexID(bare) || len(strings.TrimPrefix(bare, "@")) < rpc.OldIDLen {
		return ""
	}
	if name := handle.Words(bare, handle.MinWords); name != "" {
		return "[from figaro " + name + ", aria " + id + "]"
	}
	return "[from figaro " + id + "]"
}
