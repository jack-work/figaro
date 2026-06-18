package figaro

import (
	"github.com/jack-work/figaro/internal/message"
	"github.com/jack-work/figaro/internal/rpc"
)

// openBuilder accumulates the in-flight (unsealed) tail message from
// provider Bus events (assistant turns) or tool execution (tool_result
// turns). It is owned by the drain loop; no concurrent access.
//
// It serves both wire modes: snapshot() yields the full current IR for
// full-mode OpenEntry frames, and drainOps() yields the block-addressed
// edits since the last drain for delta-mode PatchEntry frames. The two
// are always consistent — every mutation updates the message and records
// the equivalent op.
type openBuilder struct {
	index   uint64
	version uint64 // version of the last emitted state; first emit is 1

	msg     message.Message
	toolIdx map[string]int    // tool_call_id -> block index
	partial map[int]string    // block index -> accumulated tool_invoke argument JSON
	ops     []rpc.BlockOp     // pending ops since the last drainOps
}

func newOpenBuilder(index uint64, role message.Role) *openBuilder {
	return &openBuilder{
		index:   index,
		msg:     message.Message{Role: role, LogicalTime: index},
		toolIdx: make(map[string]int),
		partial: make(map[int]string),
	}
}

// lastBlockIs reports whether the trailing block has the given type.
func (b *openBuilder) lastBlockIs(t message.ContentType) bool {
	n := len(b.msg.Content)
	return n > 0 && b.msg.Content[n-1].Type == t
}

// addText folds a text or thinking delta, extending the trailing block
// of the same kind or opening a new one.
func (b *openBuilder) addText(kind message.ContentType, text string) {
	if text == "" {
		return
	}
	if b.lastBlockIs(kind) {
		i := len(b.msg.Content) - 1
		b.msg.Content[i].Text += text
		b.ops = append(b.ops, rpc.BlockOp{Op: "append", Block: uint64(i), Text: text})
		return
	}
	b.openBlock(message.Content{Type: kind, Text: text})
}

// toolOpen opens a tool_invoke block (arguments fill in later).
func (b *openBuilder) toolOpen(id, name string) {
	i := b.openBlock(message.Content{Type: message.ContentToolInvoke, ToolCallID: id, ToolName: name})
	b.toolIdx[id] = i
}

// toolArgs accumulates streamed argument JSON for a tool_invoke block.
// In full mode this does not change the rendered message (arguments are
// only set once decoded); in delta mode it emits an append op so a
// delta-aware client can show args streaming.
func (b *openBuilder) toolArgs(id, partialJSON string) {
	i, ok := b.toolIdx[id]
	if !ok || partialJSON == "" {
		return
	}
	b.partial[i] += partialJSON
	b.ops = append(b.ops, rpc.BlockOp{Op: "append", Block: uint64(i), JSON: partialJSON})
}

// toolReady finalizes a tool_invoke block with decoded arguments.
func (b *openBuilder) toolReady(id, name string, args map[string]interface{}) {
	i, ok := b.toolIdx[id]
	if !ok {
		// PushToolReady without a prior start: open it now.
		b.toolOpen(id, name)
		i = b.toolIdx[id]
	}
	b.msg.Content[i].Arguments = args
	if name != "" {
		b.msg.Content[i].ToolName = name
	}
	c := b.msg.Content[i]
	b.ops = append(b.ops, rpc.BlockOp{Op: "replace", Block: uint64(i), Content: &c})
}

// resultOpen opens a tool_result block for a completed/streaming tool.
func (b *openBuilder) resultOpen(id, name string) {
	if _, ok := b.toolIdx[id]; ok {
		return
	}
	i := b.openBlock(message.Content{Type: message.ContentToolResult, ToolCallID: id, ToolName: name})
	b.toolIdx[id] = i
}

// resultChunk appends streamed execution output to a tool_result block.
func (b *openBuilder) resultChunk(id, chunk string) {
	i, ok := b.toolIdx[id]
	if !ok || chunk == "" {
		return
	}
	b.msg.Content[i].Text += chunk
	b.ops = append(b.ops, rpc.BlockOp{Op: "append", Block: uint64(i), Text: chunk})
}

// resultFinal replaces a tool_result block with its sealed form (the
// final result text may differ from streamed stdout, e.g. structured
// or truncated output).
func (b *openBuilder) resultFinal(id string, final message.Content) {
	i, ok := b.toolIdx[id]
	if !ok {
		i = b.openBlock(final)
		b.toolIdx[id] = i
		return
	}
	b.msg.Content[i] = final
	c := final
	b.ops = append(b.ops, rpc.BlockOp{Op: "replace", Block: uint64(i), Content: &c})
}

// openBlock appends a block and records an open op, returning its index.
func (b *openBuilder) openBlock(c message.Content) int {
	i := len(b.msg.Content)
	b.msg.Content = append(b.msg.Content, c)
	cc := c
	b.ops = append(b.ops, rpc.BlockOp{Op: "open", Block: uint64(i), Content: &cc})
	return i
}

// snapshot returns the current full IR state for a full-mode OpenEntry,
// stamping the next version.
func (b *openBuilder) snapshot() rpc.OpenEntry {
	b.version++
	b.ops = nil
	return rpc.OpenEntry{
		Index:   b.index,
		Version: b.version,
		Open:    true,
		Message: cloneMessage(b.msg),
	}
}

// drainPatch returns the ops accumulated since the last drain as a
// delta-mode PatchEntry, stamping the next version. Returns nil when
// nothing changed.
func (b *openBuilder) drainPatch() *rpc.PatchEntry {
	if len(b.ops) == 0 {
		return nil
	}
	from := b.version
	b.version++
	ops := b.ops
	b.ops = nil
	return &rpc.PatchEntry{Index: b.index, Version: b.version, From: from, Ops: ops}
}

// dirty reports whether there are unemitted changes.
func (b *openBuilder) dirty() bool { return len(b.ops) > 0 }

func cloneMessage(m message.Message) message.Message {
	out := m
	if m.Content != nil {
		out.Content = make([]message.Content, len(m.Content))
		copy(out.Content, m.Content)
	}
	return out
}
