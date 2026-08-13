package store

// How big is the OTHER cache?
//
// An open aria holds two: the decoded IR, which is windowed and reported, and
// one translation cache per provider, which is neither. `resident_ir_bytes`
// has been the number every memory table in plans/progress.md is drawn
// against, and it counts one of the two.
//
// Env-gated, points at a COPY, per house law (see realform_probe_test.go):
//
//	box=$(mktemp -d); chmod 700 "$box"
//	cp -a --reflink=auto ~/.local/state/figaro/arias "$box/arias"
//	FIGARO_PROBE_ROOT=$box/arias go test ./internal/store -run RealTranslationResidency -v

import (
	"encoding/json"
	"os"
	"sort"
	"testing"

	"github.com/jack-work/figaro/internal/message"
)

func TestRealTranslationResidency(t *testing.T) {
	root := os.Getenv("FIGARO_PROBE_ROOT")
	if root == "" {
		t.Skip("set FIGARO_PROBE_ROOT to a COPY of a real store")
	}
	be, err := NewXwalBackend(root, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer be.Close()

	type row struct {
		id          string
		irRows, irB int
		trRows, trB int
		providers   int
	}
	var rows []row
	var totIR, totTR int

	for _, n := range be.Conversations() {
		log, err := be.Open(n.ID)
		if err != nil {
			continue
		}
		irRows := log.(*cachedLog[message.Message]).Resident()
		irB := log.(*cachedLog[message.Message]).ResidentBytes()
		r := row{id: n.ID, irRows: irRows, irB: irB}
		for _, p := range []string{"anthropic", "openai", "copilot", "google"} {
			tl, err := be.OpenTranslation(n.ID, p)
			if err != nil {
				continue
			}
			c := tl.(*cachedLog[[]json.RawMessage])
			if c.Resident() == 0 {
				continue
			}
			r.providers++
			r.trRows += c.Resident()
			r.trB += c.ResidentBytes()
		}
		totIR += r.irB
		totTR += r.trB
		if r.irRows > 0 || r.trRows > 0 {
			rows = append(rows, r)
		}
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].trB > rows[j].trB })

	t.Logf("arias with residency: %d", len(rows))
	t.Logf("TOTAL   ir=%d bytes   translations=%d bytes   ratio=%.2fx",
		totIR, totTR, float64(totTR)/float64(max(totIR, 1)))
	for i, r := range rows {
		if i >= 10 {
			break
		}
		t.Logf("%s  ir=%d rows/%d B   xlt=%d rows/%d B across %d providers",
			r.id, r.irRows, r.irB, r.trRows, r.trB, r.providers)
	}
}
