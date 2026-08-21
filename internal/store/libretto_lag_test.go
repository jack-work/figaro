package store

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/internal/message"
)

// IS THE LIBRETTO BEHIND THE SOURCE AT THE MOMENT A FIG IR ENTRY IS STAMPED?
//
// That is the standing hypothesis for restartlive.sh's one-turn lag, which
// survives the commit-on-write fix and is pre-existing on main. The stamp reads
// the LIBRETTO's version (observedCursors -> formTail(LibrettoID)), and the
// libretto is a copy kept current by an ASYNCHRONOUS fold goroutine. If the
// fold has not run when the entry lands, the entry carries a stale version,
// there is no delta to render, and the change waits a turn.
//
// IT COUNTS OCCURRENCES rather than asserting a single outcome, because a race
// asserted once is a coin flip written down. A single stale stamp in many
// rounds is the finding; zero across many is evidence the fold is not the
// mechanism and the hypothesis in plans/delta-seam-rebased.md is wrong.
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
		_, err := be.ApplyForm(src, message.Patch{Set: map[string]json.RawMessage{
			"k": json.RawMessage(`"` + string(rune('a'+i%26)) + `"`),
		}})
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

// THE RESTART SHAPE: the source is patched while NOTHING IS FOLLOWING, which
// is what a daemon that is down (or an aria that is not loaded) looks like.
// The libretto then re-attaches when the aria opens, and the first entry is
// stamped immediately after.
//
// Follow's own seed is conditional -- "Seed only when the copy is BEHIND ...
// re-attaching an already-current libretto must not write the whole state
// again" -- so this asks whether the catching-up happens before the stamp or
// after it.
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
	_, err = cold.ApplyForm(src, message.Patch{Set: map[string]json.RawMessage{
		"afterrestart": json.RawMessage(`"yes"`),
	}})
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
