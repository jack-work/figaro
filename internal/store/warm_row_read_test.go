package store

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/message"
)

// A row appended must be visible to the very next read on the same handle:
// the send path catches up, then reads the conversation back out.
func TestAnAppendedRowIsVisibleToTheNextWarmRead(t *testing.T) {
	be, err := NewXwalBackend(t.TempDir(), 0)
	require.NoError(t, err)
	t.Cleanup(func() { be.Close() })

	outfit, err := be.CreateOutfit("l", message.Patch{})
	require.NoError(t, err)
	id, err := be.CreateConversation(outfit)
	require.NoError(t, err)

	ir, err := be.OpenFigIR(id)
	require.NoError(t, err)
	rec, err := ir.Append(Entry[message.Message]{Payload: message.Message{
		Role:    message.RoleInput,
		Content: []message.Content{{Type: message.ContentProse, Text: "reply with exactly: one"}},
	}})
	require.NoError(t, err)

	rows, err := be.OpenTranslator(id, "anthropic")
	require.NoError(t, err)
	require.Empty(t, rows.Read(), "a fresh aria has no rows")

	_, err = rows.Append(Entry[[]json.RawMessage]{
		FigaroLT: rec.LT,
		Payload:  []json.RawMessage{json.RawMessage(`{"role":"user"}`)},
	})
	require.NoError(t, err)

	require.Len(t, rows.Read(), 1, "the row just appended is invisible to a warm read")
}

// The daemon has two handles on one channel: the IR write path translates a
// record as it lands, and the provider reads the same channel back.
func TestARowWrittenThroughOneHandleIsVisibleThroughAnother(t *testing.T) {
	be, err := NewXwalBackend(t.TempDir(), 0)
	require.NoError(t, err)
	t.Cleanup(func() { be.Close() })

	outfit, err := be.CreateOutfit("l", message.Patch{})
	require.NoError(t, err)
	id, err := be.CreateConversation(outfit)
	require.NoError(t, err)

	ir, err := be.OpenFigIR(id)
	require.NoError(t, err)
	rec, err := ir.Append(Entry[message.Message]{Payload: message.Message{
		Role:    message.RoleInput,
		Content: []message.Content{{Type: message.ContentProse, Text: "reply with exactly: one"}},
	}})
	require.NoError(t, err)

	reader, err := be.OpenTranslator(id, "anthropic")
	require.NoError(t, err)
	require.Empty(t, reader.Read(), "a fresh aria has no rows")

	writer, err := be.OpenTranslator(id, "anthropic")
	require.NoError(t, err)
	_, err = writer.Append(Entry[[]json.RawMessage]{
		FigaroLT: rec.LT,
		Payload:  []json.RawMessage{json.RawMessage(`{"role":"user"}`)},
	})
	require.NoError(t, err)

	require.Len(t, reader.Read(), 1, "the reader's handle cannot see a row the writer's handle appended")
}

// The daemon's shape: a lineage with a fork base above 1, which is every aria
// created from an outfit. The translator channel is addressed by its own
// dense LT while the lineage's bases are main-channel LTs.
func TestATranslatorRowIsReadableOnAnAriaWhoseForkBaseIsAboveOne(t *testing.T) {
	root := t.TempDir()
	be, err := NewXwalBackend(root, 0)
	require.NoError(t, err)
	t.Cleanup(func() { be.Close() })

	outfit, err := be.CreateOutfit("l", setPatch(map[string]string{"system.model": "m"}))
	require.NoError(t, err)
	// THE PATH THE CLI TAKES: `fig new` forks the outfit node, so the aria's
	// lineage is [outfit, aria] with a base above 1. CreateConversation gives
	// a single-node lineage and cannot show this.
	id, _, err := be.ForkWith(outfit, 0, setPatch(map[string]string{"system.cwd": "/tmp"}))
	require.NoError(t, err)

	ir, err := be.OpenFigIR(id)
	require.NoError(t, err)
	var last Entry[message.Message]
	for i := 0; i < 3; i++ {
		last, err = ir.Append(Entry[message.Message]{Payload: message.Message{
			Role:    message.RoleInput,
			Content: []message.Content{{Type: message.ContentProse, Text: "turn"}},
		}})
		require.NoError(t, err)
	}
	t.Logf("fig IR tail LT = %d, lineage = %+v", last.LT, be.store.Lineage(id))

	rows, err := be.OpenTranslator(id, "anthropic")
	require.NoError(t, err)
	written, err := rows.Append(Entry[[]json.RawMessage]{
		FigaroLT: last.LT,
		Payload:  []json.RawMessage{json.RawMessage(`{"role":"user"}`)},
	})
	require.NoError(t, err)
	t.Logf("row written at channel LT %d naming fig IR LT %d", written.LT, written.FigaroLT)

	require.Len(t, rows.Read(), 1, "the provider reads an EMPTY conversation and the send fails with 'empty context'")

	// AND COLD, which is where the daemon lives: the lineage is read off the
	// topology at open, so a live backend that has not reopened cannot show
	// the fork base at all.
	be.Close()
	cold, err := NewXwalBackend(root, 0)
	require.NoError(t, err)
	t.Cleanup(func() { cold.Close() })
	coldRows, err := cold.OpenTranslator(id, "anthropic")
	require.NoError(t, err)
	t.Logf("cold lineage = %+v, Len = %d", cold.store.Lineage(id), coldRows.Len())
	require.Len(t, coldRows.Read(), 1, "a cold read attributes the row to the PARENT node, which has none")
}
