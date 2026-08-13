package store_test

// The store must know which board key names a figaro's studies in order to
// reconcile librettos from the boards, and it cannot import internal/figaro
// (figaro imports the store). So the constant is declared twice, and this is
// what makes that safe: an external test package can import both, and a
// rename on either side fails here instead of silently reconciling nothing.

import (
	"testing"

	"github.com/jack-work/figaro/internal/figaro"
	"github.com/jack-work/figaro/internal/store"
)

func TestStudiesKeyAgreesAcrossThePackageBoundary(t *testing.T) {
	if store.StudiesKey != figaro.StudiesKey {
		t.Fatalf("study key disagreement: store %q, figaro %q — the libretto "+
			"reconciliation would read nothing and report every count as zero",
			store.StudiesKey, figaro.StudiesKey)
	}
}
