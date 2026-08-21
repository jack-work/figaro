package figaro_test

// The defect this guards, verbatim from a real aria's IR (arias/ir/n714):
//
//	127 input   tool_result  toolu_01ByJoaN…
//	128 output  tool_invoke  toolu_01Tqv3GS…   <- the call
//	129 input   content:null study:{began:true} <- the mark, written from an
//	                                              RPC goroutine mid-round
//	130 input   tool_result  toolu_01Tqv3GS…   <- its result, one record late
//
// A study mark is contentless but still encodes to a user message carrying a
// system-reminder, so it DISPLACES the result by one record and the provider
// refuses the shape: "tool_use ids were found without tool_result blocks".
// It bricked two arias in this lineage.
//
// The structural rule that replaces it: no out-of-band IR record between a
// tool_use and its results. A study mark rides the inbox, so the loop writes
// it at a round boundary and it CANNOT land inside a round.

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/internal/store"
	"github.com/stretchr/testify/require"
)

func TestStudyMarkCannotLandInsideARound(t *testing.T) {
	entered, gate := newGate()
	prov := &gateProvider{name: "gate", entered: entered, gate: gate}
	a, backend, id := fuzzAgent(t, prov, nil)
	ch, unsub := subscribeChan(a)
	defer unsub()

	// A form to study, and a round parked in the provider.
	formID, _, err := backend.CreateForm("", message.Patch{
		Set: map[string]json.RawMessage{"brief": json.RawMessage(`"observed"`)},
	})
	require.NoError(t, err)

	submitPrompt(a, "the long turn")
	awaitEntered(t, entered) // the round is genuinely in flight

	studyMarks := func() int {
		lg, err := backend.OpenFigIR(id)
		require.NoError(t, err)
		n := 0
		for _, e := range lg.Read() {
			if e.Payload.Study != nil {
				n++
			}
		}
		return n
	}
	require.Equal(t, 0, studyMarks(), "a mark existed before the study")

	if _, err := a.Study(formID); err != nil {
		t.Fatalf("study while a round is in flight: %v", err)
	}

	// THE ASSERTION: while the round is parked, the mark must not be in the
	// IR. Held for long enough that a direct append from the RPC goroutine
	// would have landed -- the old code appended synchronously inside Study.
	deadline := time.Now().Add(300 * time.Millisecond)
	for time.Now().Before(deadline) {
		require.Equal(t, 0, studyMarks(),
			"a study mark landed INSIDE a provider round: it will displace a tool_result")
		time.Sleep(10 * time.Millisecond)
	}

	// The board took the study immediately, though: the mark is narration,
	// the declaration is the mechanism, and only one of them has to wait.
	studies := a.StudyList()
	require.Contains(t, studies, formID, "the study was not declared while the turn ran")

	openGate(gate, 1)
	awaitTurnDone(t, ch)

	require.Eventually(t, func() bool { return studyMarks() == 1 },
		3*time.Second, 5*time.Millisecond,
		"the mark never arrived after the round boundary")
}

// Study mints the libretto and retains it; drop gives the reference back.
// The board is the authoritative fact and the count is derived from it, so
// the two must agree the moment the verb returns -- that is what the
// reconciliation sweep exists to check, and it should have nothing to fix.
func TestStudyMintsAndRetainsItsLibretto(t *testing.T) {
	entered, gate := newGate()
	prov := &gateProvider{name: "gate", entered: entered, gate: gate}
	a, backend, _ := fuzzAgent(t, prov, nil)

	formID, _, err := backend.CreateForm("", message.Patch{
		Set: map[string]json.RawMessage{"brief": json.RawMessage(`"observed"`)},
	})
	require.NoError(t, err)

	lb, ok := backend.(interface {
		Libretto(string) (*store.Libretto, error)
		ReconcileLibrettos() (store.LibrettoAudit, error)
	})
	require.True(t, ok, "the xwal backend should carry the libretto half")

	if _, err := a.Study(formID); err != nil {
		t.Fatal(err)
	}
	lib, err := lb.Libretto(formID)
	require.NoError(t, err)
	require.Equal(t, 1, lib.Refs(), "study did not retain the libretto")
	require.Eventually(t, func() bool {
		raw, ok := lib.State().Get("brief")
		return ok && string(raw) == `"observed"`
	}, 3*time.Second, 5*time.Millisecond, "the libretto is not following the source")

	// Studying again is not a second reference: the board is a set.
	if _, err := a.Study(formID); err != nil {
		t.Fatal(err)
	}
	require.Equal(t, 1, lib.Refs(), "a repeated study double-counted")

	audit, err := lb.ReconcileLibrettos()
	require.NoError(t, err)
	require.Zero(t, audit.Corrected, "the sweep disagreed with the verb: %+v", audit)

	if _, err := a.Drop(formID); err != nil {
		t.Fatal(err)
	}
	require.Equal(t, 0, lib.Refs(), "drop did not release the libretto")
	audit, err = lb.ReconcileLibrettos()
	require.NoError(t, err)
	require.Zero(t, audit.Corrected, "after a drop the sweep disagreed: %+v", audit)
}
