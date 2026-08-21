package store

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/internal/message"
)

// THE THREE NUMBERS THAT DECIDE THE RESTART LAG.
//
// The libretto-fold hypothesis is refuted (the stamp is right), so the loss is
// downstream of the stamp. plans/delta-seam-rebased.md names what to print on
// the first turn after a restart: the watermark row's FigaroLT, the
// StudyVersions of the entry it names, and the StudyVersions of the entry
// being translated. Those decide whether the delta range is empty and why.
//
// This is a MEASUREMENT, not an assertion about the cure: it fails only if the
// range comes out empty, which is the defect, and it prints the numbers either
// way so the next reader does not have to rebuild the fixture.
func TestTheThreeNumbersBehindTheRestartLag(t *testing.T) {
	t.Run("librettos opened when the aria loads", func(t *testing.T) {
		require.True(t, restartRange(t, true) > 0,
			"the delta must survive a restart")
	})
	t.Run("librettos opened only inside the send, as the daemon used to", func(t *testing.T) {
		require.Zero(t, restartRange(t, false),
			"THIS IS THE RESTART LAG: with nothing opening the librettos before the "+
				"prompt is stamped, the entry carries the version the copy had BEFORE "+
				"the patch, the range is empty, and the change waits a turn")
	})
}

// restartRange returns how many libretto versions the first entry after a
// restart can render, which is the whole of the defect in one number.
func restartRange(t *testing.T, openLibrettoFirst bool) uint64 {
	t.Helper()
	root := t.TempDir()
	be, err := NewXwalBackend(root, 0)
	require.NoError(t, err)

	outfit, err := be.CreateOutfit("l", setPatch(map[string]string{"system.model": "m"}))
	require.NoError(t, err)
	aria, _, err := be.ForkWith(outfit, 0, setPatch(map[string]string{"system.cwd": "/tmp"}))
	require.NoError(t, err)
	src, _, err := be.CreateForm("", setPatch(map[string]string{"brief": "b0"}))
	require.NoError(t, err)
	_, err = be.Libretto(src)
	require.NoError(t, err)
	be.SetObservedForms(aria, []string{src})

	ir, err := be.OpenFigIR(aria)
	require.NoError(t, err)
	rows, err := be.OpenTranslator(aria, "anthropic")
	require.NoError(t, err)

	// A first turn: a prompt and a reply, both translated, as a real send
	// leaves the channel.
	for _, role := range []message.Role{message.RoleInput, message.RoleOutput} {
		e, aerr := ir.Append(Entry[message.Message]{Payload: message.Message{
			Role: role, Content: []message.Content{message.TextContent("turn one")},
		}})
		require.NoError(t, aerr)
		_, aerr = rows.Append(Entry[[]json.RawMessage]{
			FigaroLT: e.LT, Payload: []json.RawMessage{json.RawMessage(`{"role":"user"}`)},
		})
		require.NoError(t, aerr)
	}
	be.Close()

	// THE RESTART, and the patch that lands across it.
	cold, err := NewXwalBackend(root, 0)
	require.NoError(t, err)
	t.Cleanup(func() { cold.Close() })
	_, err = cold.ApplyForm(src, message.Patch{Set: map[string]json.RawMessage{
		"afterrestart": json.RawMessage(`"yes"`),
	}})
	require.NoError(t, err)
	cold.SetObservedForms(aria, []string{src})
	if openLibrettoFirst {
		// What resumeStudies does now: open the librettos when the aria
		// loads, so Follow has seeded before any prompt can be stamped.
		_, err = cold.Libretto(src)
		require.NoError(t, err)
	}

	coldIR, err := cold.OpenFigIR(aria)
	require.NoError(t, err)
	coldRows, err := cold.OpenTranslator(aria, "anthropic")
	require.NoError(t, err)

	next, err := coldIR.Append(Entry[message.Message]{Payload: message.Message{
		Role: message.RoleInput, Content: []message.Content{message.TextContent("turn two")},
	}})
	require.NoError(t, err)

	tail, ok := coldRows.PeekTail()
	require.True(t, ok, "the translator channel must survive the restart")
	at, ok := coldIR.Lookup(tail.FigaroLT)
	require.True(t, ok, "the entry the watermark names must be findable")

	t.Logf("1. watermark row names fig IR LT      %d", tail.FigaroLT)
	t.Logf("2. StudyVersions of THAT entry        %v   (role %s)", at.StudyVersions, at.Payload.Role)
	t.Logf("3. StudyVersions of the entry to send %v", next.StudyVersions)

	from, to := at.StudyVersions[src], next.StudyVersions[src]
	t.Logf("   => the delta range is (%d, %d]", from, to)
	return to - from
}
