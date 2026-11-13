package angelus

import (
	"testing"

	"github.com/jack-work/figaro/internal/store"
)

func AuthorityLinesForTest(t *testing.T, backend *store.XwalBackend) []string {
	return authorityLines(t, backend)
}
