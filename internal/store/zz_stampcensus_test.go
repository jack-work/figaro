package store

import (
	"os"
	"testing"

	"github.com/jack-work/figaro/internal/turns"
)

// How many real arias carry IR records with no TurnID stamp?
func TestStampCensus(t *testing.T) {
	root := os.Getenv("FIGARO_REAL_STORE")
	if root == "" {
		t.Skip("no FIGARO_REAL_STORE")
	}
	be, err := NewXwalBackend(root, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer be.Close()
	var arias, legacy, mixed, empty int
	var legacyRecs, allRecs int
	for _, n := range be.Nodes() {
		log, err := be.OpenFigIR(n.ID)
		if err != nil {
			continue
		}
		arias++
		recs := log.Read()
		if len(recs) == 0 {
			empty++
			continue
		}
		un, st := 0, 0
		for _, r := range recs {
			if !turns.Opens(r.Payload) {
				continue // a record outside every turn carries 0 by design
			}
			if r.Payload.TurnID == 0 {
				un++
			} else {
				st++
			}
		}
		allRecs += st + un
		legacyRecs += un
		switch {
		case st == 0 && un == 0:
			empty++
			continue
		case st == 0:
			legacy++
		case un > 0:
			mixed++
		}
	}
	t.Logf("arias=%d empty=%d fully-unstamped=%d mixed=%d ; records=%d unstamped=%d",
		arias, empty, legacy, mixed, allRecs, legacyRecs)
}
