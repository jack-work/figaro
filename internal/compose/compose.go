// Package compose maps a turn's message.Message blocks to livedoc nodes.
// It must stay free of renderer dependencies: the agent composes too.
package compose

import (
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/jack-work/figaro/api/livedoc"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/internal/turns"
)

// nodeID keys an id-less node by its IR coordinate (LT, block index), both
// of which are stable; a render position is not.
func nodeID(lt uint64, blockIdx int) string {
	return fmt.Sprintf("%d.%d", lt, blockIdx)
}

// composeBashCap bounds the source lines of tool output a node carries.
const composeBashCap = 200

type ToolTiming struct {
	// OpenedAt is when the tool block opened on the provider stream;
	// StartedAt/FinishedAt bracket execution.
	OpenedAt   int64
	StartedAt  int64
	FinishedAt int64
}

// Nodes maps a turn's messages to the live node list. argPartials carries the
// still-streaming tool_use argument JSON per tool_call_id.
func Nodes(msgs []message.Message, partials, argPartials map[string]string, timings ...map[string]ToolTiming) []livedoc.Node {
	results := indexResults(msgs)
	var toolTimings map[string]ToolTiming
	if len(timings) > 0 {
		toolTimings = timings[0]
	}
	var nodes []livedoc.Node
	for _, m := range msgs {
		if m.Role == message.RoleInput {
			if turns.IsSteering(m) {
				for ci, c := range m.Content {
					if c.Type == message.ContentProse && strings.TrimSpace(c.Text) != "" {
						n := textNode(livedoc.NodeSteering, roleInput, m.LogicalTime, ci, m.Timestamp, c.Text)
						n.Sender = c.Sender
						nodes = append(nodes, n)
					}
				}
			}
			continue
		}
		if m.Role != message.RoleOutput {
			continue
		}
		for ci, c := range m.Content {
			// Empty prose/thinking blocks are minted, not skipped: a block that
			// fills later must already own its position.
			switch c.Type {
			case message.ContentProse:
				nodes = append(nodes, textNode(livedoc.NodeProse, roleOutput, m.LogicalTime, ci, m.Timestamp, c.Text))
			case message.ContentThinking:
				nodes = append(nodes, textNode(livedoc.NodeThinking, roleOutput, m.LogicalTime, ci, m.Timestamp, c.Text))
			case message.ContentToolInvoke:
				nodes = append(nodes, toolNode(c, m.LogicalTime, ci, m.Timestamp, results, partials, argPartials, toolTimings))
			}
		}
	}
	return nodes
}

const (
	roleInput  = livedoc.RoleInput
	roleOutput = livedoc.RoleOutput
)

func textNode(t livedoc.NodeType, role string, lt uint64, block int, at int64, text string) livedoc.Node {
	return livedoc.Node{
		ID:       nodeID(lt, block),
		Type:     t,
		Role:     role,
		LTs:      []uint64{lt},
		Src:      []livedoc.Src{{LT: lt, Block: block}},
		At:       at,
		Markdown: strings.TrimRight(text, "\n"),
	}
}

func toolNode(inv message.Content, lt uint64, block int, at int64, results map[string]resultAt, partials, argPartials map[string]string, timings map[string]ToolTiming) livedoc.Node {
	name := inv.ToolName
	if name == "" {
		name = "tool"
	}
	args := inv.Arguments
	// A quarantined call shows the raw bytes, not the MalformedArgs envelope.
	rawArgs, quarantined := message.MalformedArgsOf(inv)
	if quarantined {
		args = nil
	}
	n := livedoc.Node{
		Type:       livedoc.NodeTool,
		ID:         inv.ToolCallID,
		ToolCallID: inv.ToolCallID,
		Role:       roleOutput,
		LTs:        []uint64{lt},
		Src:        []livedoc.Src{{LT: lt, Block: block}},
		At:         at,
		Name:       name,
		Args:       args,
		Summary:    summaryFor(args),
	}
	if quarantined {
		n.Input = tailBound(rawArgs)
	}
	if timing, ok := timings[inv.ToolCallID]; ok {
		n.OpenedAt = timing.OpenedAt
		n.StartedAt = timing.StartedAt
		n.FinishedAt = timing.FinishedAt
	} else {
		n.StartedAt = at
	}
	if got, done := results[inv.ToolCallID]; done {
		res := got.Content
		n.Src = append(n.Src, got.Src)
		if got.Src.LT != lt {
			n.LTs = append(n.LTs, got.Src.LT)
		}
		if n.FinishedAt == 0 {
			n.FinishedAt = got.At
		}
		n.Status = livedoc.StatusOK
		if res.IsError {
			n.Status = livedoc.StatusError
		}
		n.Output, n.OutputBase = tailBoundAt(res.Text)
	} else {
		n.Status = livedoc.StatusRunning
		n.Output, n.OutputBase = tailBoundAt(partials[inv.ToolCallID])
		if len(args) == 0 && !quarantined {
			n.Input = tailBound(argPartials[inv.ToolCallID])
		}
	}
	return n
}

// summaryFor feeds search and the clipboard, never rendering.
func summaryFor(args map[string]any) string {
	if len(args) == 0 {
		return ""
	}
	keys := make([]string, 0, len(args))
	for k := range args {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%v", k, args[k]))
	}
	return strings.Join(parts, " ")
}

func tailBound(text string) string {
	out, _ := tailBoundAt(text)
	return out
}

// tailBoundAt also returns the rune offset of the kept tail within text, so
// a quote of the preview resolves against the whole block.
func tailBoundAt(text string) (string, int) {
	if text == "" {
		return "", 0
	}
	s := strings.TrimRight(text, "\n")
	cut := len(s)
	for i := 0; i < composeBashCap; i++ {
		j := strings.LastIndexByte(s[:cut], '\n')
		if j < 0 {
			return s, 0
		}
		cut = j
	}
	return s[cut+1:], utf8.RuneCountInString(s[:cut+1])
}

type resultAt struct {
	Content message.Content
	Src     livedoc.Src
	At      int64
}

func indexResults(msgs []message.Message) map[string]resultAt {
	out := map[string]resultAt{}
	for _, m := range msgs {
		for ci, c := range m.Content {
			if c.Type == message.ContentToolResult && c.ToolCallID != "" {
				out[c.ToolCallID] = resultAt{
					Content: c, Src: livedoc.Src{LT: m.LogicalTime, Block: ci}, At: m.Timestamp,
				}
			}
		}
	}
	return out
}
