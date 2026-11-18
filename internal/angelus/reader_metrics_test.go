package angelus

import (
	"testing"

	"github.com/jack-work/figaro/internal/livelog/aria"
	"github.com/jack-work/figaro/internal/store"
)

// A dormant figaro's page carries the metrics its agent shows once woken: the
// sidecar, with the records past its watermark folded in. For a figaro whose
// sidecar is a fold of its history that is the fold; an interior fork's
// sidecar holds no inherited token sums, and neither does its page.
func TestADormantPageCarriesTheSidecarsMetrics(t *testing.T) {
	f := newAdoptFixture(t, 6)
	child := f.fork(t, 4)
	for _, id := range []string{f.parent, child} {
		r := NewAriaReaderBounded(f.backend, f.h.reader().proj, f.a.UICache)
		page, err := r.Page(id, aria.Anchor{}, aria.Anchor{}, 1<<20, true)
		if err != nil {
			t.Fatal(err)
		}
		meta, err := f.backend.Meta(id)
		if err != nil {
			t.Fatal(err)
		}
		held := *meta
		log, err := f.backend.OpenFigIR(id)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range log.Read() {
			if e.LT > held.LastFigaroLT {
				held.Fold(e)
			}
		}
		want := aria.Metrics{
			ContextTokens: held.ContextTokens, ContextExact: held.ContextExact,
			TokensIn: held.TokensIn, TokensOut: held.TokensOut,
			CacheReadTokens: held.CacheReadTokens, CacheWriteTokens: held.CacheWriteTokens,
		}
		got := *page.Metrics
		got.Mantra = ""
		if got != want {
			t.Fatalf("%s: page metrics %+v, sidecar %+v", id, got, want)
		}
	}
	var whole store.AriaMeta
	log, _ := f.backend.OpenFigIR(f.parent)
	for _, e := range log.Read() {
		whole.Fold(e)
	}
	r := NewAriaReaderBounded(f.backend, f.h.reader().proj, f.a.UICache)
	page, _ := r.Page(f.parent, aria.Anchor{}, aria.Anchor{}, 1<<20, true)
	if page.Metrics.TokensIn != whole.TokensIn || page.Metrics.ContextTokens != whole.ContextTokens {
		t.Fatalf("the parent's sidecar is a fold of its history: page %+v, fold %+v", *page.Metrics, whole)
	}
}
