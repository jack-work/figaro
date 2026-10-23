package figaro_test

// Concurrent casts onto one figaro, counted on both sides of the write. The
// loss wants one caster descheduled between its durable write and its mirror
// publish, so GOMAXPROCS is pinned low and confidence comes from rounds, not width.

import (
	"context"
	"encoding/json"
	"os"
	"runtime"
	"strconv"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/internal/figaro"
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

// mirrorRoundLoss is one round's verdict per side.
type mirrorRoundLoss struct {
	round        int
	bound        int
	mirror       int
	lostFromMap  []string
	lostFromDisk []string
}

func TestConcurrentCastsKeepEveryStudyInTheMirror(t *testing.T) {
	casts := mirrorRaceEnv(t, "FIG_MIRROR_RACE_N", mirrorRaceCastsDefault, 2)
	rounds := mirrorRaceEnv(t, "FIG_MIRROR_RACE_ROUNDS", mirrorRaceRoundsDefault, 1)
	procs := mirrorRaceEnv(t, "FIG_MIRROR_RACE_P", mirrorRaceProcsDefault, 1)

	// Restored before the package's parallel tests run.
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
	lostStudies, boundFormLosses := 0, 0
	for _, l := range losses {
		lostStudies += len(l.lostFromMap)
		boundFormLosses += len(l.lostFromDisk)
	}
	t.Fatalf("studies were lost in %d of %d rounds of %d concurrent casts (GOMAXPROCS=%d): "+
		"%d lost from the MIRROR a turn renders from, %d lost from the DURABLE bound form.\n"+
		"MIRROR losses are the unguarded whole-set a.form.Apply in declareStudy: last writer wins, "+
		"and the last writer is not the last durable write.\n"+
		"BOARD losses would mean the version-guarded compare-and-set itself leaks, which is a "+
		"different and worse defect.\nrounds: %+v",
		len(losses), rounds, casts, procs, lostStudies, boundFormLosses, losses)
}

// mirrorRaceRound casts N roles at once and reports what each side holds.
func mirrorRaceRound(t *testing.T, round, casts int) mirrorRoundLoss {
	t.Helper()
	entered, gate := newGate()
	prov := &gateProvider{name: "gate", entered: entered, gate: gate}
	a, backend, ariaID := fuzzAgent(t, prov, nil)

	roles := make([]string, casts)
	for i := range roles {
		id, _, err := backend.CreateForm("", form.Build(form.Snapshot{}, map[string]json.RawMessage{"name": json.RawMessage(`"role"`)}, nil))
		require.NoError(t, err)
		roles[i] = id
	}

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
	bound := figaro.StudiesFromSnapshot(durable) // what the sweep and a revival read

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
	// A side holding more than it was given is also a loss: counts, not membership.
	require.Lenf(t, bound, casts,
		"round %d: the durable bound form holds %d entries for %d casts: %v", round, len(bound), casts, bound)
	require.LessOrEqualf(t, len(mirror), casts,
		"round %d: the mirror holds %d entries for %d casts: %v", round, len(mirror), casts, mirror)

	return mirrorRoundLoss{
		round: round, bound: len(bound), mirror: len(mirror),
		lostFromMap: missing(mirror), lostFromDisk: missing(bound),
	}
}
