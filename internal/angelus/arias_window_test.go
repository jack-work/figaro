package angelus

import (
	"fmt"
	"testing"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/recent"
)

func windowFixture(n int) []rpc.FigaroInfoResponse {
	rows := make([]rpc.FigaroInfoResponse, n)
	for i := range rows {
		rows[i] = rpc.FigaroInfoResponse{ID: fmt.Sprintf("a%04d", i), Handle: fmt.Sprintf("h%04d", i), LastActive: int64(1000 + i)}
	}
	return rows
}

func TestTheWebWindowKeepsTheNewestRowsAndCountsTheRest(t *testing.T) {
	rows := windowFixture(500)
	kept, more := newestRows(rows, 200)
	if len(kept) != 200 || more != 300 {
		t.Fatalf("kept %d more %d", len(kept), more)
	}
	if kept[0].ID != "a0499" || kept[199].ID != "a0300" {
		t.Fatalf("window is not the newest by active_at: %s..%s", kept[0].ID, kept[199].ID)
	}
	if rows[0].ID != "a0000" {
		t.Fatal("the window reordered the caller's rows")
	}
}

func TestTheArialWindowKeepsAncestorsOfKeptRows(t *testing.T) {
	rows := windowFixture(500)
	rows[0].LastActive = 1
	rows[10].Parent = rows[0].ID
	rows[499].Parent = rows[10].ID
	kept, more := newestWithAncestors(rows, 200)
	if len(kept) != 202 || more != 298 {
		t.Fatalf("kept %d more %d", len(kept), more)
	}
	snap := ariasSnapshot(kept, 0, more)
	if v, ok := snap.Get("more"); !ok || string(v) != "298" {
		t.Fatalf("root more = %s", v)
	}
	if _, ok := snap.Get("arias.h0000.forks.h0010.forks.h0499.id"); !ok {
		t.Fatal("a kept fork lost its ancestors")
	}
}

func TestNothingIsCutBelowTheWindow(t *testing.T) {
	rows := windowFixture(200)
	if kept, more := newestWithAncestors(rows, 200); len(kept) != 200 || more != 0 {
		t.Fatalf("kept %d more %d", len(kept), more)
	}
	if _, ok := ariasSnapshot(rows, 0, 0).Get("more"); ok {
		t.Fatal("more is set with nothing cut")
	}
	if _, ok := recent.Snapshot(recent.Rows(rows), 0).Get("more"); ok {
		t.Fatal("recent more is set with nothing cut")
	}
}

func TestAnAriaNamedMoreDoesNotCollideWithTheCutCount(t *testing.T) {
	rows := windowFixture(3)
	rows[2].Handle = "more-beans"
	snap := recent.Snapshot(recent.Rows(rows), 7)
	if v, ok := snap.Get(recent.KeyMore); !ok || string(v) != "7" {
		t.Fatalf("root more = %s", v)
	}
	if _, ok := snap.Get("a0002." + recent.KeyID); !ok {
		t.Fatal("the aria named more did not fall back to its id")
	}
}
