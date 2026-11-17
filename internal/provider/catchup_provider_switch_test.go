package provider

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/internal/arialog"
)

// Issue 112: a grandparent wrote rows on a provider, a fork inherited them,
// and a later fork that switched to that provider cleared its channel (a
// fingerprint change) and rebuilt it. Its channel then owns everything from
// 1 while the chain above still carries a higher base. Every turn after the
// rebuild failed with ErrViewHidesOwnRows; each must now catch up.
func TestCatchUpAfterAProviderSwitchRebuildsOnAFork(t *testing.T) {
	f := openBatchFixture(t, t.TempDir(), 4096)
	f.newAria(t, 30)
	if _, err := CatchUp(f.config(t, batchedChannel)); err != nil {
		t.Fatal(err)
	}

	for i, records := range []int{12, 9} {
		child, _, err := f.backend.ForkWith(f.aria, 0, form.Build(form.Snapshot{}, map[string]json.RawMessage{"mantra": json.RawMessage(`"fork"`)}, nil))
		if err != nil {
			t.Fatal(err)
		}
		f.aria = child
		f.openLog(t)
		for j := 0; j < records; j++ {
			f.appendRecord(t, 100*(i+1)+j)
		}
		if i == 0 {
			if _, err := CatchUp(f.config(t, batchedChannel)); err != nil {
				t.Fatal(err)
			}
		}
	}

	cfg := f.config(t, batchedChannel)
	cfg.Fingerprint = "test/v2"
	if _, cleared, err := ClearStaleTranslationCache(cfg.Translator, cfg.Fingerprint); err != nil || !cleared {
		t.Fatalf("switch: cleared %v, err %v", cleared, err)
	}
	irLen := f.log.Len()
	for turn := 0; turn < 3; turn++ {
		stats, err := CatchUp(cfg)
		if err != nil {
			if errors.Is(err, arialog.ErrViewHidesOwnRows) {
				t.Fatalf("turn %d after the switch: %v", turn, err)
			}
			t.Fatal(err)
		}
		if turn > 0 && stats.Visited != 1 {
			t.Fatalf("turn %d re-derived %d records, want only the new one", turn, stats.Visited)
		}
		f.appendRecord(t, 500+turn)
		irLen++
	}
	if _, err := CatchUp(cfg); err != nil {
		t.Fatal(err)
	}
	if got := f.log.Len(); got != irLen {
		t.Fatalf("IR holds %d records, want %d", got, irLen)
	}
	arialog.ScanAll(cfg.Translator, func(e arialog.Entry[[]json.RawMessage]) bool {
		if e.Fingerprint != "test/v2" {
			t.Fatalf("row %d (figaro lt %d) is an ancestor's, fingerprint %q", e.LT, e.FigaroLT, e.Fingerprint)
		}
		return true
	})
}
