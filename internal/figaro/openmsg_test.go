package figaro

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/internal/message"
	"github.com/jack-work/figaro/internal/rpc"
)

func TestOpenBuilder_TextAccumulates(t *testing.T) {
	b := newOpenBuilder(5, message.RoleAssistant)
	b.addText(message.ContentText, "Hel")
	b.addText(message.ContentText, "lo")

	open, _ := b.commit()
	assert.Equal(t, uint64(5), open.Index)
	assert.Equal(t, uint64(1), open.Version)
	assert.True(t, open.Open)
	require.Len(t, open.Message.Content, 1)
	assert.Equal(t, "Hello", open.Message.Content[0].Text)
}

func TestOpenBuilder_InterleavedBlocks(t *testing.T) {
	b := newOpenBuilder(1, message.RoleAssistant)
	b.addText(message.ContentText, "thinking about it: ")
	b.toolOpen("tc_1", "bash")
	b.toolReady("tc_1", "bash", map[string]interface{}{"command": "ls"})
	b.addText(message.ContentText, "done")

	open, _ := b.commit()
	m := open.Message
	require.Len(t, m.Content, 3)
	assert.Equal(t, message.ContentText, m.Content[0].Type)
	assert.Equal(t, message.ContentToolInvoke, m.Content[1].Type)
	assert.Equal(t, "bash", m.Content[1].ToolName)
	assert.Equal(t, "ls", m.Content[1].Arguments["command"])
	assert.Equal(t, message.ContentText, m.Content[2].Type)
	assert.Equal(t, "done", m.Content[2].Text)
}

func TestOpenBuilder_CommitVersioning(t *testing.T) {
	b := newOpenBuilder(2, message.RoleAssistant)
	b.addText(message.ContentText, "ab")

	open1, p1 := b.commit()
	assert.Equal(t, uint64(1), open1.Version)
	require.NotNil(t, p1)
	assert.Equal(t, uint64(1), p1.Version)
	assert.Equal(t, uint64(0), p1.From)
	// A new block opens with its first chunk embedded — one op.
	require.Len(t, p1.Ops, 1)
	assert.Equal(t, "open", p1.Ops[0].Op)
	require.NotNil(t, p1.Ops[0].Content)
	assert.Equal(t, "ab", p1.Ops[0].Content.Text)

	// No changes since last commit → nil patch (version still advances).
	_, pNil := b.commit()
	assert.Nil(t, pNil)

	b.addText(message.ContentText, "cd")
	open3, p3 := b.commit()
	assert.Equal(t, uint64(3), open3.Version)
	require.NotNil(t, p3)
	assert.Equal(t, uint64(3), p3.Version)
	assert.Equal(t, uint64(2), p3.From)
	require.Len(t, p3.Ops, 1)
	assert.Equal(t, "append", p3.Ops[0].Op)
	assert.Equal(t, "cd", p3.Ops[0].Text)
}

func TestOpenBuilder_ToolResult(t *testing.T) {
	b := newOpenBuilder(3, message.RoleUser)
	b.resultOpen("tc_1", "bash")
	b.resultChunk("tc_1", "line1\n")
	b.resultChunk("tc_1", "line2\n")
	b.resultFinal("tc_1", message.ToolResultContent("tc_1", "bash", "line1\nline2\n", false))

	open, _ := b.commit()
	m := open.Message
	require.Len(t, m.Content, 1)
	assert.Equal(t, message.ContentToolResult, m.Content[0].Type)
	assert.Equal(t, "line1\nline2\n", m.Content[0].Text)
	assert.False(t, m.Content[0].IsError)
}

func TestOpenBuilder_CommitIsCopy(t *testing.T) {
	b := newOpenBuilder(1, message.RoleAssistant)
	b.addText(message.ContentText, "x")
	open, _ := b.commit()
	b.addText(message.ContentText, "y")
	// The earlier snapshot must not see the later mutation.
	assert.Equal(t, "x", open.Message.Content[0].Text)
}

// applyOps folds delta-mode ops into a message, mirroring a delta client.
func applyOps(m *message.Message, ops []rpc.BlockOp) {
	for _, op := range ops {
		switch op.Op {
		case "open":
			if op.Content != nil {
				for uint64(len(m.Content)) <= op.Block {
					m.Content = append(m.Content, message.Content{})
				}
				m.Content[op.Block] = *op.Content
			}
		case "append":
			if op.Block < uint64(len(m.Content)) {
				m.Content[op.Block].Text += op.Text
			}
		case "replace":
			if op.Content != nil && op.Block < uint64(len(m.Content)) {
				m.Content[op.Block] = *op.Content
			}
		}
	}
}

// TestOpenBuilder_DeltaReconstructsFull proves a delta client folding
// the patch ops arrives at the same message a full-mode client gets.
func TestOpenBuilder_DeltaReconstructsFull(t *testing.T) {
	b := newOpenBuilder(2, message.RoleAssistant)
	var reconstructed message.Message

	b.addText(message.ContentText, "Hel")
	_, p := b.commit()
	require.NotNil(t, p)
	applyOps(&reconstructed, p.Ops)

	b.addText(message.ContentText, "lo")
	b.toolOpen("tc_1", "bash")
	b.toolReady("tc_1", "bash", map[string]interface{}{"command": "ls"})
	full, p := b.commit()
	require.NotNil(t, p)
	applyOps(&reconstructed, p.Ops)

	assert.Equal(t, full.Message.Content, reconstructed.Content)
}
