package store

import (
	"fmt"
	"os"
	"testing"
	"time"
)

// A real store is the only place the two migrations meet: the layout moves
// (v3 -> v4) while the IR records stay canonical, so an aria written before
// the cursor stamp is read through the migration-era fallback ON A STORE
// THAT WAS JUST MOVED. Nothing synthetic covers that pair.
//
// It is skipped unless FIGARO_MIGRATED_STORE names a COPY of one. Never
// point it at ~/.local/state/figaro/arias: it opens the store for writing.
//
// It asserts counts and structure only. No conversation content is read,
// logged or compared -- the numbers are the evidence, and they are all that
// leaves this test.
func TestRealMigratedStoreOpensWithEverything(t *testing.T) {
	root := os.Getenv("FIGARO_MIGRATED_STORE")
	if root == "" {
		t.Skip("set FIGARO_MIGRATED_STORE to a COPY of a real store")
	}
	want := 0
	if v := os.Getenv("FIGARO_MIGRATED_ARIAS"); v != "" {
		if _, err := fmt.Sscanf(v, "%d", &want); err != nil {
			t.Fatal(err)
		}
	}

	be, err := NewXwalBackend(root, 0)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer be.Close()

	convs := be.Conversations()
	t.Logf("conversations=%d nodes=%d", len(convs), len(be.Nodes()))
	if len(convs) == 0 {
		t.Fatal("no conversations: the store opened empty, which is the failure this exists to catch")
	}
	if want > 0 && len(convs) != want {
		t.Errorf("conversations=%d, want %d", len(convs), want)
	}

	// FAILURES ARE COLLECTED, NOT FATAL. A walk that stops at the first bad
	// aria answers "is there one?" and cannot answer "how many?" -- and the
	// second question is the one that says whether a store has a defect or a
	// speck. Every failure below is counted and the ids are reported in
	// bounded number.
	var (
		read, boards, entries int
		deepest               string
		deepestDepth          int
		openFailed            []string
		formFailed            []string
	)
	for _, n := range convs {
		lg, err := be.Open(n.ID)
		if err != nil {
			openFailed = append(openFailed, fmt.Sprintf("%s: %v", n.ID, err))
			continue
		}
		got := len(lg.ReadFrom(0, 0))
		entries += got
		if got > 0 {
			read++
		}
		snap, err := be.FormState(n.ID)
		if err != nil {
			formFailed = append(formFailed, fmt.Sprintf("%s: %v", n.ID, err))
			continue
		}
		if snap.Len() > 0 {
			boards++
		}
		if d := nodeChainDepth(be, n.ID); d > deepestDepth {
			deepest, deepestDepth = n.ID, d
		}
	}
	t.Logf("arias with entries=%d/%d total_entries=%d arias_with_a_board=%d deepest_node_chain=%d",
		read, len(convs), entries, boards, deepestDepth)
	// KNOWN, PRE-EXISTING DAMAGE IS TOLERATED BY COUNT, NEW DAMAGE IS NOT.
	// realform_probe_test.go recorded the same five arias in 2026-08 --
	// "index not found" for their form channel, boards never written -- and
	// they read that way in the LIVE store, so a copy is not the cause. They
	// are TAINTED under the standing rule that serialized data does not
	// constrain the design; what this test owes is to notice a SIXTH.
	allowed := 0
	if v := os.Getenv("FIGARO_PROBE_ALLOW_UNREADABLE"); v != "" {
		if _, err := fmt.Sscanf(v, "%d", &allowed); err != nil {
			t.Fatalf("FIGARO_PROBE_ALLOW_UNREADABLE: %v", err)
		}
	}
	if len(openFailed) > 0 {
		t.Errorf("open failed on %d/%d arias", len(openFailed), len(convs))
		for _, s := range firstN(openFailed, 5) {
			t.Errorf("  open: %s", s)
		}
	}
	if len(formFailed) > allowed {
		t.Errorf("form failed on %d/%d arias, tolerating %d (set FIGARO_PROBE_ALLOW_UNREADABLE)",
			len(formFailed), len(convs), allowed)
		for _, s := range firstN(formFailed, 5) {
			t.Errorf("  form: %s", s)
		}
	} else if len(formFailed) > 0 {
		t.Logf("form failed on %d/%d arias, within the tolerated %d: %v",
			len(formFailed), len(convs), allowed, firstN(formFailed, 5))
	}

	// The cost flattening makes explicit: a node chain is walked by opening
	// each ancestor's log, per channel, per read. Nesting hid it inside the
	// directory walk. Reported, never asserted -- a duration assertion
	// passes here and fails on someone else's filesystem. Nor is this a
	// cold-cache number: the store is open and the pages are warm.
	if deepest != "" {
		start := time.Now()
		lg, err := be.Open(deepest)
		if err != nil {
			t.Fatal(err)
		}
		n := len(lg.ReadFrom(0, 0))
		t.Logf("deepest aria: chain=%d entries=%d read_after_open=%s",
			deepestDepth, n, time.Since(start).Round(time.Millisecond))
	}
	if read == 0 {
		t.Error("every aria read empty")
	}
	if boards == 0 {
		t.Error("not one aria has a form: the cursor fallback is not reaching the old keyed records")
	}
	_ = deepest
}

// nodeChainDepth counts the NODES behind an aria, not the trunks figaro
// shows. In the nested layout a continuation forked a child that kept the
// trunk id, so one aria can stand on a chain of them; flat, that chain is
// walked by opening each ancestor's log, per channel, on every cold read.
func nodeChainDepth(be *XwalBackend, ariaID string) int {
	nodes := be.Store().trunks.Nodes()
	head, ok := be.Store().trunks.HeadNode(ariaID)
	if !ok {
		return 0
	}
	depth := 0
	for cur := head; ; depth++ {
		n, ok := nodes[cur]
		if !ok || n.From == "" || n.From == cur {
			return depth
		}
		cur = n.From
	}
}

func firstN(in []string, n int) []string {
	if len(in) <= n {
		return in
	}
	return in[:n]
}
