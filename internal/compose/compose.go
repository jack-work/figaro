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
	"sort"
	"strings"

	"github.com/jack-work/figaro/internal/livedoc"
	"github.com/jack-work/figaro/internal/message"
	"github.com/jack-work/figaro/internal/turns"
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

// ToolPreviewArg returns the name of the "body" argument whose live-streaming
// value should surface as a running tool node's preview (e.g. "content" for
type ToolTiming struct {
	// OpenedAt is when the tool block opened on the provider stream — the
	// start of GENERATION. StartedAt/FinishedAt bracket EXECUTION.
	OpenedAt   int64
	StartedAt  int64
	FinishedAt int64
}

// Nodes maps a turn's messages to the live node list: each assistant
// content block becomes a node in order — text/thinking → prose, tool
// invoke → a tool node folding in its result (or streamed partial). A
// tool with no result yet is left status=running with whatever output has
// streamed. argPartials carries the raw, still-truncated tool_use argument
// JSON per tool_call_id; it becomes the node's Input, for every tool alike.
func Nodes(msgs []message.Message, partials, argPartials map[string]string, timings ...map[string]ToolTiming) []livedoc.Node {
	results := indexResults(msgs)
	var toolTimings map[string]ToolTiming
	if len(timings) > 0 {
		toolTimings = timings[0]
	}
	var nodes []livedoc.Node
	for _, m := range msgs {
		if m.Role == message.RoleInput {
			// A steering interjection is a direction aimed at the turn already in
			// flight. Two accepted shapes for the one concept: the drain's
			// explicit flag, and the legacy shape where prose rides on the
			// tool_result message. Either way it becomes one steering node,
			// positioned where it arrived (after the tool nodes). tool_result
			// blocks themselves fold under their invoke (indexResults).
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
			continue // tool_result messages fold under their invoke; user prompts aren't in the turn
		}
		for ci, c := range m.Content {
			// Empty prose/thinking blocks are minted, not skipped: a block that
			// arrives empty and fills later must already own its position, or the
			// nodes after it would shift under it. The renderer hides empties.
			switch c.Type {
			case message.ContentProse:
				nodes = append(nodes, textNode(livedoc.NodeProse, roleOutput, m.LogicalTime, ci, m.Timestamp, c.Text))
			case message.ContentThinking:
				nodes = append(nodes, textNode(livedoc.NodeThinking, roleOutput, m.LogicalTime, ci, m.Timestamp, c.Text))
			case message.ContentToolInvoke:
				nodes = append(nodes, toolNode(c, m.LogicalTime, ci, results, partials, argPartials, toolTimings))
			}
		}
	}
	return nodes
}

const (
	roleInput  = livedoc.RoleInput
	roleOutput = livedoc.RoleOutput
)

// textNode builds a prose/thinking/steering node at one fig-IR coordinate.
// at is the source message's wall-clock timestamp, carried through so the UI
// can show WHEN a node was written the way it already shows tool timings.
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

func toolNode(inv message.Content, lt uint64, block int, results map[string]resultAt, partials, argPartials map[string]string, timings map[string]ToolTiming) livedoc.Node {
	name := inv.ToolName
	if name == "" {
		name = "tool"
	}
	args := inv.Arguments
	// A QUARANTINED CALL IS SHOWN AS THE BYTES THAT ARRIVED. Its arguments
	// never parsed, so figaro wrapped them in an envelope to keep the wire
	// legal (message.MalformedArgs) — but that envelope is bookkeeping, not
	// something a reader asked to see, and rendering it would put a sentinel
	// key where the arguments belong AND hide the very bytes worth looking at.
	//
	// So the envelope is unwrapped back into Input, which is the field for
	// exactly this: the raw, still-unparsed argument text. Unlike the live
	// prefix it survives a reload, because it travels on the message rather
	// than in the turn's scratch map — the failed call reads the same tomorrow
	// as it did while it was streaming.
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
	}
	if got, done := results[inv.ToolCallID]; done {
		res := got.Content
		// A tool node spans two coordinates: the invoke and its result, in
		// different messages at different block indices.
		n.Src = append(n.Src, got.Src)
		if got.Src.LT != lt {
			n.LTs = append(n.LTs, got.Src.LT)
		}
		n.Status = livedoc.StatusOK
		if res.IsError {
			n.Status = livedoc.StatusError
		}
		n.Output = tailBound(res.Text)
	} else {
		n.Status = livedoc.StatusRunning
		n.Output = tailBound(partials[inv.ToolCallID])
		// Generation phase: the arguments are still arriving. Show the raw
		// prefix for EVERY tool — no name is consulted and none is special —
		// and drop it the moment the decoded Arguments land, since Args says
		// the same thing without the truncation.
		if len(args) == 0 && !quarantined {
			n.Input = tailBound(argPartials[inv.ToolCallID])
		}
	}
	return n
}

// summaryFor is a tool node's one-line description, used for SEARCH and for
// the clipboard — never for rendering, which reads the arguments directly
// through the CLI's tool table. It is deliberately generic: the per-tool
// Summarize() hooks it used to call said which argument spoke for a call,
// which is the same thing the table says, in a second place.
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

// Units is gone. compose.Turns is the single projection now — a turn is one
// exchange, opened by Turn.Inquiry (text) rather than by a unit or a node. See
// internal/compose/turns.go and docs/turn-addressing.md.

// resultAt is a tool_result block together with the coordinate it was found
// at, so a tool node can record both of its sources.
type resultAt struct {
	Content message.Content
	Src     livedoc.Src
}

func indexResults(msgs []message.Message) map[string]resultAt {
	out := map[string]resultAt{}
	for _, m := range msgs {
		for ci, c := range m.Content {
			if c.Type == message.ContentToolResult && c.ToolCallID != "" {
				out[c.ToolCallID] = resultAt{Content: c, Src: livedoc.Src{LT: m.LogicalTime, Block: ci}}
			}
		}
	}
	return out
}
