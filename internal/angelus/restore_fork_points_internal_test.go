package angelus

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/jack-work/figaro/internal/store"
)

// P6, the migration half: restore the fork points on a COPY of a store, then
// TestAuthorityDump on the same copy must write what it wrote before.
// FIGARO_P6_RESTORE names the copy; FIGARO_P6_OUT gets one line per node the
// restore looked at, ids only.
func TestRestoreForkPointsOnACopy(t *testing.T) {
	src, out := os.Getenv("FIGARO_P6_RESTORE"), os.Getenv("FIGARO_P6_OUT")
	if src == "" || out == "" {
		t.Skip("FIGARO_P6_RESTORE names a copy of a store, FIGARO_P6_OUT the report")
	}
	backend, err := store.NewXwalBackend(src, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()
	report, err := backend.Store().RestoreForkPoints(false)
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	restored := 0
	for _, r := range report {
		if r.Kept == "" {
			restored++
		}
		fmt.Fprintf(&b, "node %s trunk %s under %s origin %s kept %q\n", r.Node, r.Trunk, r.Under, r.Origin, r.Kept)
	}
	if err := os.WriteFile(out, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Logf("P6 restore: looked at %d, restored %d, kept %d", len(report), restored, len(report)-restored)
}
