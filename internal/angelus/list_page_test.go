package angelus

// PAGING TESTS.
//
// A paged listing fails silently. A cursor that is off by one row serves that
// row twice, or never serves it at all, and the reader has no way to tell:
// every page looks plausible on its own. So the boundaries get tested
// exhaustively rather than sampled.

import (
	"strconv"
	"testing"

	"github.com/jack-work/figaro/api/rpc"
)

func listRow(id string, millis int64) rpc.FigaroInfoResponse {
	return rpc.FigaroInfoResponse{ID: id, LastActive: millis}
}

// walk pages the whole set and returns the ids in the order they were served,
// which is the only thing a reader actually experiences.
func walkPages(t *testing.T, all []rpc.FigaroInfoResponse, limit int) []string {
	t.Helper()
	var seen []string
	cursor := ""
	for i := 0; ; i++ {
		if i > 100 {
			t.Fatal("paging did not terminate: NextBefore never came back empty")
		}
		// Fresh copy each call: boundPage sorts in place, and a real daemon
		// rebuilds the row set per request.
		rows := append([]rpc.FigaroInfoResponse(nil), all...)
		page, next, err := boundPage(rows, rpc.ListRequest{Limit: limit, Before: cursor})
		if err != nil {
			t.Fatalf("page %d: %v", i, err)
		}
		for _, r := range page {
			seen = append(seen, r.ID)
		}
		if next == "" {
			return seen
		}
		if len(page) == 0 {
			t.Fatal("a page was empty but promised a successor")
		}
		cursor = next
	}
}

// EVERY ROW EXACTLY ONCE, IN ORDER. The property that matters, checked across
// every page size against a set that does not divide evenly.
func TestPagingServesEveryRowExactlyOnce(t *testing.T) {
	var all []rpc.FigaroInfoResponse
	for i := 0; i < 17; i++ {
		// Descending recency, so expected order is insertion order.
		all = append(all, listRow("aria"+strconv.Itoa(i), int64(1000-i)))
	}
	want := make([]string, len(all))
	for i, r := range all {
		want[i] = r.ID
	}

	for limit := 1; limit <= 20; limit++ {
		got := walkPages(t, all, limit)
		if len(got) != len(want) {
			t.Fatalf("limit %d: served %d rows, want %d (%v)", limit, len(got), len(want), got)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("limit %d: position %d served %s, want %s", limit, i, got[i], want[i])
			}
		}
	}
}

// THE TIE-BREAK IS WHY THIS WORKS. Rows sharing a millisecond are ordered
// arbitrarily without it, and an arbitrary order is not stable between calls --
// so a cursor lands in the middle of the tied group and the same row is served
// twice while another is lost. Every row here shares one timestamp.
func TestPagingIsStableWhenEveryRowSharesATimestamp(t *testing.T) {
	var all []rpc.FigaroInfoResponse
	for i := 0; i < 10; i++ {
		all = append(all, listRow("aria"+strconv.Itoa(i), 500))
	}
	got := walkPages(t, all, 3)
	if len(got) != 10 {
		t.Fatalf("served %d of 10 tied rows: %v", len(got), got)
	}
	seen := map[string]int{}
	for _, id := range got {
		seen[id]++
	}
	for id, n := range seen {
		if n != 1 {
			t.Errorf("%s served %d times; the id tie-break is not holding", id, n)
		}
	}
}

// An unbounded request must behave exactly as it did before this feature: no
// sort, no cursor, every row.
func TestNoLimitAndNoCursorIsTheHistoricalAnswer(t *testing.T) {
	all := []rpc.FigaroInfoResponse{listRow("b", 1), listRow("a", 2)}
	page, next, err := boundPage(append([]rpc.FigaroInfoResponse(nil), all...), rpc.ListRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if next != "" {
		t.Fatalf("an unbounded request offered a cursor: %q", next)
	}
	if len(page) != 2 || page[0].ID != "b" {
		t.Fatalf("an unbounded request reordered or dropped rows: %+v", page)
	}
}

// Exhaustion is signalled by an EMPTY cursor and nothing else. A page that
// happens to be full but has no successors must still end the walk.
func TestExactMultipleOfLimitTerminates(t *testing.T) {
	all := []rpc.FigaroInfoResponse{listRow("a", 3), listRow("b", 2), listRow("c", 1)}
	got := walkPages(t, all, 3)
	if len(got) != 3 {
		t.Fatalf("served %v", got)
	}
}

// A MALFORMED CURSOR IS AN ERROR, not a silent restart. A reader who believes
// they are on page four and is quietly handed page one has been lied to, and
// will never find out.
func TestMalformedCursorRefuses(t *testing.T) {
	for _, bad := range []string{"garbage", "notanumber:aria1", ":", "123"} {
		_, _, err := boundPage([]rpc.FigaroInfoResponse{listRow("a", 1)},
			rpc.ListRequest{Limit: 2, Before: bad})
		if err == nil {
			t.Errorf("cursor %q was accepted; it must refuse rather than restart", bad)
		}
	}
}

// A cursor naming a row that has since vanished must still page correctly:
// arias get killed between taps.
func TestCursorSurvivesAVanishedRow(t *testing.T) {
	all := []rpc.FigaroInfoResponse{listRow("a", 5), listRow("b", 4), listRow("c", 3)}
	_, next, err := boundPage(append([]rpc.FigaroInfoResponse(nil), all...), rpc.ListRequest{Limit: 1})
	if err != nil || next == "" {
		t.Fatalf("first page: next=%q err=%v", next, err)
	}
	// "a" is gone now.
	page, _, err := boundPage([]rpc.FigaroInfoResponse{listRow("b", 4), listRow("c", 3)},
		rpc.ListRequest{Limit: 2, Before: next})
	if err != nil {
		t.Fatal(err)
	}
	if len(page) != 2 || page[0].ID != "b" {
		t.Fatalf("a cursor naming a dead row did not resume correctly: %+v", page)
	}
}

// The cursor must be short enough for a Telegram callback_data (64 bytes),
// which is what kept it out of a server-side session.
func TestCursorFitsInACallbackPayload(t *testing.T) {
	c := listCursor(listRow("deadbeef", 1788000000000))
	if len(c) > 40 {
		t.Fatalf("cursor is %d bytes (%q); it shares 64 with a command prefix", len(c), c)
	}
	ms, id, err := parseListCursor(c)
	if err != nil || ms != 1788000000000 || id != "deadbeef" {
		t.Fatalf("cursor did not round-trip: %d %q %v", ms, id, err)
	}
}

// listOrder and afterCursor must agree. If they drift, a boundary either
// repeats a row or loses one, and no single page looks wrong.
func TestOrderAndCursorAgree(t *testing.T) {
	rows := []rpc.FigaroInfoResponse{
		listRow("b", 5), listRow("a", 5), listRow("c", 4), listRow("a", 9),
	}
	for _, pivot := range rows {
		ms, id, _ := parseListCursor(listCursor(pivot))
		for _, r := range rows {
			cmp := listOrder(r, pivot)
			after := afterCursor(r, ms, id)
			if (cmp > 0) != after {
				t.Errorf("row %s/%d: listOrder says %d but afterCursor says %v, relative to %s/%d",
					r.ID, r.LastActive, cmp, after, pivot.ID, pivot.LastActive)
			}
		}
	}
}
