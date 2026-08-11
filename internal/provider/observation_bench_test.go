package provider_test

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/jack-work/figaro/internal/form"
	"github.com/jack-work/figaro/internal/message"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/store"
)

// What OBSERVATION costs, per translated turn, as the observed set grows.
//
// The unification made every IR append stamp the whole observed set's
// positions, and the translator derives each member's patch-fold between
// consecutive stamps. That derivation runs on EVERY translate — warm and cold,
// for every provider — so its slope in the number of observed forms is the
// number that decides whether a figaro can study many things or few.
//
// Commissioned when the storm harness went to 50 concurrent arias on one
// role: correctness at that scale was measured live, and this is the other
// half — what one aria pays per turn for each thing it watches.

// benchForm is a Form accessor over a fixed patch history.
type benchForm struct {
	patches []message.Patch
}

func (f *benchForm) PatchesBetween(after, upTo uint64) []message.Patch {
	if upTo > uint64(len(f.patches)) {
		upTo = uint64(len(f.patches))
	}
	if after >= upTo {
		return nil
	}
	return f.patches[after:upTo]
}

func benchPatches(n int) []message.Patch {
	out := make([]message.Patch, n)
	for i := range out {
		out[i] = message.Patch{Set: map[string]json.RawMessage{
			"brief": json.RawMessage(fmt.Sprintf(`"version %d of the brief"`, i)),
		}}
	}
	return out
}

// projectWith runs one incremental projection over `turns` entries, each
// stamping `observed` forms.
func projectWith(b *testing.B, turns, observed int, warm bool) {
	b.Helper()
	studies := map[string]provider.Form{}
	stamp := map[string]uint64{}
	for i := 0; i < observed; i++ {
		id := fmt.Sprintf("@form%03d", i)
		studies[id] = &benchForm{patches: benchPatches(turns + 1)}
		stamp[id] = 0
	}
	log := store.NewMemLog[message.Message]()
	for t := 0; t < turns; t++ {
		versions := map[string]uint64{}
		for id := range studies {
			versions[id] = uint64(t + 1) // one patch per form per turn
		}
		_, _ = log.Append(store.Entry[message.Message]{
			Payload:       message.Message{Role: message.RoleInput},
			StudyVersions: versions,
		})
	}

	cfg := provider.ProjectionConfig[int]{
		Log:     log,
		Studies: studies,
		Encode: func(m message.Message, _ form.Snapshot) ([]json.RawMessage, error) {
			// Encode is the provider's business; count the studied folds so
			// the compiler cannot elide the derivation.
			n := len(m.StudyPatches)
			return []json.RawMessage{json.RawMessage(fmt.Sprintf(`{"n":%d}`, n))}, nil
		},
		Append: func(s int, enc []json.RawMessage, _ uint64) int { return s + len(enc) },
	}

	var previous *provider.IncrementalProjection[int]
	if warm {
		p, _, err := provider.ProjectIncrementally(cfg)
		if err != nil {
			b.Fatal(err)
		}
		previous = p
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		cfg.Previous = previous
		if _, _, err := provider.ProjectIncrementally(cfg); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkObservationCold0(b *testing.B)  { projectWith(b, 40, 0, false) }
func BenchmarkObservationCold1(b *testing.B)  { projectWith(b, 40, 1, false) }
func BenchmarkObservationCold8(b *testing.B)  { projectWith(b, 40, 8, false) }
func BenchmarkObservationCold50(b *testing.B) { projectWith(b, 40, 50, false) }

// Warm is the shape a live turn actually takes: the prefix is translated, one
// entry is new. If the observed set costs anything HERE it costs it on every
// turn forever.
func BenchmarkObservationWarm0(b *testing.B)  { projectWith(b, 40, 0, true) }
func BenchmarkObservationWarm1(b *testing.B)  { projectWith(b, 40, 1, true) }
func BenchmarkObservationWarm8(b *testing.B)  { projectWith(b, 40, 8, true) }
func BenchmarkObservationWarm50(b *testing.B) { projectWith(b, 40, 50, true) }
