package figaro_test

// THE INSTRUMENT FOR THE LOST STUDY.
//
// TestConcurrentCastsOfOneFigaro (self_cast_test.go) failed once in
// twenty-one runs on a loaded box with "a concurrent cast lost its study",
// and a contains-check that samples eight roles tells you only THAT one went
// missing, never how many or on which side of the write. This is the same
// hazard measured instead of sampled:
//
//   - the assertion is a COUNT of every role, on BOTH sides of the write;
//   - failure names the missing ids and both holdings, because the shape of
//     the loss IS the diagnosis: durable short means the version-guarded
//     compare-and-set leaks, mirror short means the unguarded whole-set
//     a.form.Apply in declareStudy is losing writes to last-writer-wins;
//   - many independent ROUNDS, because one round of a probabilistic loss is
//     a coin, and the gate must not depend on the coin.
//
// WHAT MADE IT RED, measured and not guessed. A sweep of N (8, 32, 64, 256)
// under a box at loadavg 27-34 was green in forty runs. The loss does not
// want a big N and it does not want a busy machine: it wants ONE CASTER
// DESCHEDULED between its durable write returning and its mirror publish,
// which is a window microseconds wide. GOMAXPROCS=1 forces exactly that, and
// at GOMAXPROCS=1, N=8 it went red 2 times in 10 -- while GOMAXPROCS=1, N=64
// stayed green, so a wider N is not the lever and reporting it as one would
// have been the wrong instrument. The test therefore pins GOMAXPROCS low for
// its own duration and pays for its confidence in rounds, not in width.
//
// See ~/notes/figaro/cast-lost-study-window.md. Run under -race.

import (
	"context"
	"encoding/json"
	"os"
	"runtime"
	"strconv"
	"testing"
	"time"

	"github.com/jack-work/figaro/internal/figaro"
	"github.com/jack-work/figaro/internal/form"
	"github.com/stretchr/testify/require"
)

const (
	mirrorRaceCastsDefault  = 8  // the width the window actually likes
	mirrorRaceRoundsDefault = 24 // p(loss) per round is ~0.1; see the rate below
	mirrorRaceProcsDefault  = 1  // the lever: preemption, not load
)

func mirrorRaceEnv(t *testing.T, key string, def, min int) int {
	t.Helper()
	raw := os.Getenv(key)
	if raw == "" {
		return def
	}
	n, err := strconv.Atoi(raw)
	require.NoErrorf(t, err, "%s is not a number: %q", key, raw)
	require.GreaterOrEqualf(t, n, min, "%s must be at least %d", key, min)
	return n
}

// mirrorRoundLoss is one round's verdict: what each side held, and which
// roles the side lost. Kept per round so the failure message can report the
// RATE and the SIZE of the loss rather than the first symptom.
type mirrorRoundLoss struct {
	round        int
	board        int
	mirror       int
	lostFromMap  []string
	lostFromDisk []string
}

func TestConcurrentCastsKeepEveryStudyInTheMirror(t *testing.T) {
	casts := mirrorRaceEnv(t, "FIG_MIRROR_RACE_N", mirrorRaceCastsDefault, 2)
	rounds := mirrorRaceEnv(t, "FIG_MIRROR_RACE_ROUNDS", mirrorRaceRoundsDefault, 1)
	procs := mirrorRaceEnv(t, "FIG_MIRROR_RACE_P", mirrorRaceProcsDefault, 1)

	// Restored before the package's parallel tests run: this pin is for the
	// window, not for the suite.
	defer runtime.GOMAXPROCS(runtime.GOMAXPROCS(procs))

	var losses []mirrorRoundLoss
	for round := 1; round <= rounds; round++ {
		loss := mirrorRaceRound(t, round, casts)
		if len(loss.lostFromMap) > 0 || len(loss.lostFromDisk) > 0 {
			losses = append(losses, loss)
		}
	}
	if len(losses) == 0 {
		return
	}
	lostStudies, boardLosses := 0, 0
	for _, l := range losses {
		lostStudies += len(l.lostFromMap)
		boardLosses += len(l.lostFromDisk)
	}
	t.Fatalf("studies were lost in %d of %d rounds of %d concurrent casts (GOMAXPROCS=%d): "+
		"%d lost from the MIRROR a turn renders from, %d lost from the DURABLE board.\n"+
		"MIRROR losses are the unguarded whole-set a.form.Apply in declareStudy: last writer wins, "+
		"and the last writer is not the last durable write.\n"+
		"BOARD losses would mean the version-guarded compare-and-set itself leaks, which is a "+
		"different and worse defect.\nrounds: %+v",
		len(losses), rounds, casts, procs, lostStudies, boardLosses, losses)
}

// mirrorRaceRound casts N roles onto one figaro at once and reports what each
// side of the write ended up holding.
func mirrorRaceRound(t *testing.T, round, casts int) mirrorRoundLoss {
	t.Helper()
	entered, gate := newGate()
	prov := &gateProvider{name: "gate", entered: entered, gate: gate}
	a, backend, ariaID := fuzzAgent(t, prov, nil)

	roles := make([]string, casts)
	for i := range roles {
		id, _, err := backend.CreateForm("", form.Patch{
			Set: map[string]json.RawMessage{"name": json.RawMessage(`"role"`)},
		})
		require.NoError(t, err)
		roles[i] = id
	}

	// All N released together: they contend on ONE board (the study set) and
	// on N distinct role forms.
	start := make(chan struct{})
	errs := make(chan error, casts)
	for _, roleID := range roles {
		go func(roleID string) {
			<-start
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			_, err := a.Cast(ctx, roleID, nil)
			errs <- err
		}(roleID)
	}
	close(start)
	for range roles {
		require.NoErrorf(t, <-errs, "round %d: a cast failed outright", round)
	}

	mirror := a.StudyList() // what a TURN renders from
	durable, err := backend.FormState(ariaID)
	require.NoError(t, err)
	board := figaro.StudiesFromSnapshot(durable) // what the sweep and a revival read

	missing := func(have []string) []string {
		seen := make(map[string]bool, len(have))
		for _, id := range have {
			seen[id] = true
		}
		var lost []string
		for _, id := range roles {
			if !seen[id] {
				lost = append(lost, id)
			}
		}
		return lost
	}
	// A side holding MORE than it was given is also a loss of a different
	// kind, and it must not read as success: the counts are asserted, not
	// only the membership.
	require.Lenf(t, board, casts,
		"round %d: the durable board holds %d entries for %d casts: %v", round, len(board), casts, board)
	require.LessOrEqualf(t, len(mirror), casts,
		"round %d: the mirror holds %d entries for %d casts: %v", round, len(mirror), casts, mirror)

	return mirrorRoundLoss{
		round: round, board: len(board), mirror: len(mirror),
		lostFromMap: missing(mirror), lostFromDisk: missing(board),
	}
}
