// Package compose maps the Figaro IR (a turn's message.Message blocks)
// to the canonical live-render unit: an ordered list of typed nodes. It
// is the producer-side translation, analogous to a provider Encode —
// pure, deterministic, and dependency-light (no renderer/glamour), so the
// agent can compose without importing the terminal renderer.
//
// Block → node mapping (each assistant content block is one node, in
// order, so an edit to one block localizes to a single node op):
//   - text      → prose node (markdown)
//   - thinking  → thinking node (rendered dim/blockquote by the client)
//   - tool_invoke → tool node {name, args, status, output}; its result
//     (or streamed partial) folds in as output, status running→ok/error
//
// The spinner is the consumer's concern (animated locally per running
// tool); compose emits no sentinel. Tool-result messages (user role) fold
// under their invoke via tool_call_id; the user's own prompt is a
// separate committed unit and is not part of the agent turn.
package compose

import (
	"fmt"
	"strings"

	"github.com/jack-work/figaro/internal/livedoc"
	"github.com/jack-work/figaro/internal/message"
)

// nodeID mints a stable id for an id-less node (thinking/prose/steering) from
// its primary-IR coordinate: the message's logical time and the content
// block's index within that message. Both are stable — LT is immutable once
// assigned and the block index is append-only — so the id doesn't move when
// the flattened, empty-skipped render position shifts. (Tool nodes key off
// their ToolCallID instead.) The in-flight message gets a provisional LT from
// composeTurn so its ids match what they'll be after it seals.
func nodeID(lt uint64, blockIdx int) string {
	return fmt.Sprintf("%d.%d", lt, blockIdx)
}

// composeBashCap bounds how many source lines of tool output a node
// carries; the renderer further clamps the display. Full output lives in
// the canonical Content IR.
const composeBashCap = 200

// Nodes maps a turn's messages to the live node list: each assistant
// content block becomes a node in order — text/thinking → prose, tool
// invoke → a tool node folding in its result (or streamed partial). A
// tool with no result yet is left status=running with whatever output has
// streamed.
func Nodes(msgs []message.Message, partials map[string]string) []livedoc.Node {
	results := indexResults(msgs)
	var nodes []livedoc.Node
	for _, m := range msgs {
		if m.Role == message.RoleUser {
			// A user message inside a turn group is a tool_result tic; its
			// tool_result blocks fold under their invoke (indexResults). If it
			// ALSO carries text, that's a steering interjection — emit it as a
			// node, positioned where it arrived (after the tool nodes).
			if hasToolResult(m) {
				for ci, c := range m.Content {
					if c.Type == message.ContentProse && strings.TrimSpace(c.Text) != "" {
						nodes = append(nodes, livedoc.Node{ID: nodeID(m.LogicalTime, ci), Type: livedoc.NodeSteering, Markdown: strings.TrimRight(c.Text, "\n")})
					}
				}
			}
			continue
		}
		if m.Role != message.RoleAssistant {
			continue // tool_result messages fold under their invoke; user prompts aren't in the turn
		}
		for ci, c := range m.Content {
			switch c.Type {
			case message.ContentProse:
				if strings.TrimSpace(c.Text) == "" {
					continue
				}
				nodes = append(nodes, livedoc.Node{ID: nodeID(m.LogicalTime, ci), Type: livedoc.NodeProse, Markdown: strings.TrimRight(c.Text, "\n")})
			case message.ContentThinking:
				if strings.TrimSpace(c.Text) == "" {
					continue
				}
				nodes = append(nodes, livedoc.Node{ID: nodeID(m.LogicalTime, ci), Type: livedoc.NodeThinking, Markdown: strings.TrimRight(c.Text, "\n")})
			case message.ContentToolInvoke:
				nodes = append(nodes, toolNode(c, results, partials))
			}
		}
	}
	return nodes
}

func toolNode(inv message.Content, results map[string]message.Content, partials map[string]string) livedoc.Node {
	name := inv.ToolName
	if name == "" {
		name = "tool"
	}
	n := livedoc.Node{
		Type: livedoc.NodeTool,
		ID:   inv.ToolCallID,
		Name: name,
		Args: inv.Arguments,
	}
	if res, done := results[inv.ToolCallID]; done {
		n.Status = livedoc.StatusOK
		if res.IsError {
			n.Status = livedoc.StatusError
		}
		n.Output = tailBound(res.Text)
	} else {
		n.Status = livedoc.StatusRunning
		n.Output = tailBound(partials[inv.ToolCallID])
	}
	return n
}

// tailBound clamps streamed tool output to the last composeBashCap source
// lines; the full result stays in the canonical Content IR.
func tailBound(text string) string {
	if text == "" {
		return ""
	}
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	if len(lines) > composeBashCap {
		lines = lines[len(lines)-composeBashCap:]
	}
	return strings.Join(lines, "\n")
}

// Unit is one committed conversational unit: a user prompt or an
// assistant turn, as a typed node list. LT is the figwal main-LT of the
// unit's last message — the coordinate `send`/`fork <trunk>:<LT>` address —
// so a renderer can label units with the LT a fork would target.
type Unit struct {
	Role  string         `json:"role"`
	Nodes []livedoc.Node `json:"nodes"`
	LT    uint64         `json:"lt,omitempty"`
}

// Units folds a message log into committed conversational units in
// order, mirroring the live wire's segmentation: each text-bearing user
// message is its own prompt unit (one prose node), and the assistant
// messages following it (with their tool results) compose into one turn
// unit. A catch-up read replays these to reproduce the same scrollback
// the live stream would have produced.
func Units(msgs []message.Message) []Unit {
	var units []Unit
	var group []message.Message
	flush := func() {
		if len(group) == 0 {
			return
		}
		if nodes := Nodes(group, nil); len(nodes) > 0 {
			units = append(units, Unit{Role: "assistant", Nodes: nodes, LT: group[len(group)-1].LogicalTime})
		}
		group = nil
	}
	for _, m := range msgs {
		// A pure-text user message starts a new prompt unit. A user message
		// carrying a tool_result stays in the turn group even if it also has
		// text (a steering interjection) — that text becomes a steering node.
		if m.Role == message.RoleUser && !hasToolResult(m) {
			if txt := messageText(m); txt != "" {
				flush()
				units = append(units, Unit{Role: "user", Nodes: []livedoc.Node{{Type: livedoc.NodeProse, Markdown: txt}}, LT: m.LogicalTime})
			}
		}
		group = append(group, m)
	}
	flush()
	return units
}

// messageText joins a message's text blocks; "" when it carries none
// (e.g. a tool-result message or a control-only patch).
func messageText(m message.Message) string {
	var parts []string
	for _, c := range m.Content {
		if c.Type == message.ContentProse && strings.TrimSpace(c.Text) != "" {
			parts = append(parts, c.Text)
		}
	}
	return strings.Join(parts, "\n")
}

// hasToolResult reports whether a message carries any tool_result block — i.e.
// it's a tool-result tic (part of the turn) rather than a fresh user prompt.
func hasToolResult(m message.Message) bool {
	for _, c := range m.Content {
		if c.Type == message.ContentToolResult {
			return true
		}
	}
	return false
}

func indexResults(msgs []message.Message) map[string]message.Content {
	out := map[string]message.Content{}
	for _, m := range msgs {
		for _, c := range m.Content {
			if c.Type == message.ContentToolResult && c.ToolCallID != "" {
				out[c.ToolCallID] = c
			}
		}
	}
	return out
}
