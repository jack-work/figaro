package store

import (
	"encoding/json"
	"github.com/jack-work/figaro/api/form"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/message"
)

// Is the libretto behind the source at the moment an IR entry is stamped? The
// stamp reads the libretto's version, and the libretto is kept current by an
// asynchronous fold; a stale stamp leaves no delta to render for a turn.
// Counted over many rounds, since a race asserted once is a coin flip.
func TestWhetherTheLibrettoIsBehindWhenAnEntryIsStamped(t *testing.T) {
	be, err := NewXwalBackend(t.TempDir(), 0)
	require.NoError(t, err)
	t.Cleanup(func() { be.Close() })

	outfit, err := be.CreateOutfit("l", setPatch(map[string]string{"system.model": "m"}))
	require.NoError(t, err)
	aria, _, err := be.ForkWith(outfit, 0, setPatch(map[string]string{"system.cwd": "/tmp"}))
	require.NoError(t, err)

	src, _, err := be.CreateForm("", setPatch(map[string]string{"k0": "v0"}))
	require.NoError(t, err)
	lib, err := be.Libretto(src)
	require.NoError(t, err)
	require.NotNil(t, lib)
	be.SetObservedForms(aria, []string{src})

	ir, err := be.OpenFigIR(aria)
	require.NoError(t, err)

	const rounds = 40
	stale := 0
	for i := 0; i < rounds; i++ {
		// Patch the SOURCE, then stamp an entry as immediately as a send does.
		_, err := be.ApplyForm(src, form.Build(form.Snapshot{}, map[string]json.RawMessage{
			"k": json.RawMessage(`"` + string(rune('a'+i%26)) + `"`),
		}, nil))
		require.NoError(t, err)

		srcAt, ok := be.store.formTail(src)
		require.True(t, ok)

		e, err := ir.Append(Entry[message.Message]{Payload: message.Message{
			Role: message.RoleInput, Content: []message.Content{message.TextContent("prompt")},
		}})
		require.NoError(t, err)

		stamped := e.StudyVersions[src]
		libAt, _ := be.store.formTail(LibrettoID(src))
		if stamped < uint64(i+1) || libAt < srcAt {
			stale++
			if stale == 1 {
				t.Logf("round %d: source at %d, LIBRETTO at %d, entry stamped %d",
					i, srcAt, libAt, stamped)
			}
		}
	}

	t.Logf("STALE STAMPS: %d of %d rounds (source patched, entry appended immediately after)",
		stale, rounds)
	if stale > 0 {
		t.Logf("THE FOLD IS BEHIND AT STAMP TIME. That is the mechanism behind the " +
			"one-turn lag: no delta exists yet when the entry is written, so the " +
			"change rides the next entry.")
	} else {
		t.Logf("THE FOLD IS NOT BEHIND HERE. The hypothesis in " +
			"plans/delta-seam-rebased.md does not explain the restart lag, and the " +
			"cause is elsewhere -- which is what a previous bearer already suspected.")
	}
}

// The restart shape: the source is patched while nothing is following, then
// the libretto re-attaches and the first entry is stamped immediately after.
// Follow seeds only when the copy is behind, so this asks whether catching up
// happens before the stamp.
func TestTheLibrettoAfterAReattachIsCurrentBeforeTheNextStamp(t *testing.T) {
	root := t.TempDir()
	be, err := NewXwalBackend(root, 0)
	require.NoError(t, err)

	outfit, err := be.CreateOutfit("l", setPatch(map[string]string{"system.model": "m"}))
	require.NoError(t, err)
	aria, _, err := be.ForkWith(outfit, 0, setPatch(map[string]string{"system.cwd": "/tmp"}))
	require.NoError(t, err)
	src, _, err := be.CreateForm("", setPatch(map[string]string{"k0": "v0"}))
	require.NoError(t, err)
	_, err = be.Libretto(src)
	require.NoError(t, err)
	be.SetObservedForms(aria, []string{src})

	ir, err := be.OpenFigIR(aria)
	require.NoError(t, err)
	_, err = ir.Append(Entry[message.Message]{Payload: message.Message{
		Role: message.RoleInput, Content: []message.Content{message.TextContent("first")},
	}})
	require.NoError(t, err)
	be.Close() // the daemon stops

	// THE PATCH LANDS WITH NOTHING FOLLOWING.
	cold, err := NewXwalBackend(root, 0)
	require.NoError(t, err)
	t.Cleanup(func() { cold.Close() })
	_, err = cold.ApplyForm(src, form.Build(form.Snapshot{}, map[string]json.RawMessage{
		"afterrestart": json.RawMessage(`"yes"`),
	}, nil))
	require.NoError(t, err)
	srcAt, _ := cold.store.formTail(src)

	// The aria comes back: the libretto re-attaches, the observed set is
	// re-declared, and a prompt lands -- the order a restarted daemon takes.
	_, err = cold.Libretto(src)
	require.NoError(t, err)
	cold.SetObservedForms(aria, []string{src})

	coldIR, err := cold.OpenFigIR(aria)
	require.NoError(t, err)
	e, err := coldIR.Append(Entry[message.Message]{Payload: message.Message{
		Role: message.RoleInput, Content: []message.Content{message.TextContent("after restart")},
	}})
	require.NoError(t, err)

	libAt, _ := cold.store.formTail(LibrettoID(src))
	t.Logf("source at %d, libretto at %d, the first entry after the restart stamped %d",
		srcAt, libAt, e.StudyVersions[src])

	require.NotZero(t, e.StudyVersions[src],
		"the first entry after a restart carries NO study cursor at all")
	require.GreaterOrEqual(t, libAt, uint64(2),
		"the libretto did not catch up before the first entry was stamped: the patch "+
			"applied while nothing was following is not in the copy the stamp reads, "+
			"so there is no delta to render and the change waits a turn")
}
