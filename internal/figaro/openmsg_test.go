package figaro

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/internal/message"
)

func TestOpenBuilder_TextAccumulates(t *testing.T) {
	b := newOpenBuilder(5, message.RoleAssistant)
	b.addText(message.ContentText, "Hel")
	b.addText(message.ContentText, "lo")

	snap := b.snapshot()
	assert.Equal(t, uint64(5), snap.Index)
	assert.Equal(t, uint64(1), snap.Version)
	assert.True(t, snap.Open)
	require.Len(t, snap.Message.Content, 1)
	assert.Equal(t, "Hello", snap.Message.Content[0].Text)
}

func TestOpenBuilder_InterleavedBlocks(t *testing.T) {
	b := newOpenBuilder(1, message.RoleAssistant)
	b.addText(message.ContentText, "thinking about it: ")
	b.toolOpen("tc_1", "bash")
	b.toolReady("tc_1", "bash", map[string]interface{}{"command": "ls"})
	b.addText(message.ContentText, "done")

	m := b.snapshot().Message
	require.Len(t, m.Content, 3)
	assert.Equal(t, message.ContentText, m.Content[0].Type)
	assert.Equal(t, message.ContentToolInvoke, m.Content[1].Type)
	assert.Equal(t, "bash", m.Content[1].ToolName)
	assert.Equal(t, "ls", m.Content[1].Arguments["command"])
	assert.Equal(t, message.ContentText, m.Content[2].Type)
	assert.Equal(t, "done", m.Content[2].Text)
}

func TestOpenBuilder_DrainPatchVersioning(t *testing.T) {
	b := newOpenBuilder(2, message.RoleAssistant)
	b.addText(message.ContentText, "ab")

	p1 := b.drainPatch()
	require.NotNil(t, p1)
	assert.Equal(t, uint64(1), p1.Version)
	assert.Equal(t, uint64(0), p1.From)
	// A new block opens with its first chunk embedded — one op.
	require.Len(t, p1.Ops, 1)
	assert.Equal(t, "open", p1.Ops[0].Op)
	require.NotNil(t, p1.Ops[0].Content)
	assert.Equal(t, "ab", p1.Ops[0].Content.Text)

	// No changes since last drain → nil.
	assert.Nil(t, b.drainPatch())

	b.addText(message.ContentText, "cd")
	p2 := b.drainPatch()
	require.NotNil(t, p2)
	assert.Equal(t, uint64(2), p2.Version)
	assert.Equal(t, uint64(1), p2.From)
	require.Len(t, p2.Ops, 1)
	assert.Equal(t, "append", p2.Ops[0].Op)
	assert.Equal(t, "cd", p2.Ops[0].Text)
}

func TestOpenBuilder_ToolResult(t *testing.T) {
	b := newOpenBuilder(3, message.RoleUser)
	b.resultOpen("tc_1", "bash")
	b.resultChunk("tc_1", "line1\n")
	b.resultChunk("tc_1", "line2\n")
	b.resultFinal("tc_1", message.ToolResultContent("tc_1", "bash", "line1\nline2\n", false))

	m := b.snapshot().Message
	require.Len(t, m.Content, 1)
	assert.Equal(t, message.ContentToolResult, m.Content[0].Type)
	assert.Equal(t, "line1\nline2\n", m.Content[0].Text)
	assert.False(t, m.Content[0].IsError)
}

func TestOpenBuilder_SnapshotIsCopy(t *testing.T) {
	b := newOpenBuilder(1, message.RoleAssistant)
	b.addText(message.ContentText, "x")
	snap := b.snapshot()
	b.addText(message.ContentText, "y")
	// The earlier snapshot must not see the later mutation.
	assert.Equal(t, "x", snap.Message.Content[0].Text)
}
