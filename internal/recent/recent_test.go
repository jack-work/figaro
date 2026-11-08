package recent

import (
	"testing"

	"github.com/jack-work/figaro/api/rpc"
)

func fig(id, handle, mantra string, active int64) rpc.FigaroInfoResponse {
	return rpc.FigaroInfoResponse{ID: id, Handle: handle, Mantra: mantra, LastActive: active, State: "dormant"}
}

func names(rows []Row) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.Name
	}
	return out
}

func same(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestRowsAreNewestFirst(t *testing.T) {
	rows := Rows([]rpc.FigaroInfoResponse{
		fig("a1", "ox-one", "", 100),
		fig("b2", "pushup-turtle", "", 300),
		fig("c3", "egomaniac-rhino", "", 200),
		fig("d4", "quiet-moon", "", 0),
	})
	if got := names(rows); !same(got, []string{"pushup", "egomaniac", "ox", "quiet"}) {
		t.Fatalf("order = %v, want most recently active first, never-active last", got)
	}
}

// Equal activity falls back to the id, so two listings of the same store
// agree row for row.
func TestATieIsBrokenByID(t *testing.T) {
	rows := Rows([]rpc.FigaroInfoResponse{fig("b", "bee-two", "", 5), fig("a", "ant-one", "", 5)})
	if got := names(rows); !same(got, []string{"ant", "bee"}) {
		t.Fatalf("order = %v, want the lower id first on a tie", got)
	}
}

// A row is named by the first word of its handle; a word two rows share
// would name neither, so both take their whole handle.
func TestAnameIsTheHandlesFirstWordUnlessShared(t *testing.T) {
	rows := Rows([]rpc.FigaroInfoResponse{
		fig("1", "pushup-turtle", "", 3),
		fig("2", "pushup-banana", "", 2),
		fig("3", "egomaniac-rhino", "", 1),
		fig("4", "", "", 0),
	})
	if got := names(rows); !same(got, []string{"pushup-turtle", "pushup-banana", "egomaniac", "4"}) {
		t.Fatalf("names = %v", got)
	}
}

func TestTheTitleIsTheMantraElseTheHandle(t *testing.T) {
	rows := Rows([]rpc.FigaroInfoResponse{
		fig("1", "pushup-turtle", "steering the lanes", 2),
		fig("2", "ox-one", "", 1),
		fig("3", "", "", 0),
	})
	want := []string{"steering the lanes", "ox-one", "3"}
	for i, r := range rows {
		if r.Title != want[i] {
			t.Errorf("row %s title = %q, want %q", r.Name, r.Title, want[i])
		}
	}
}

func TestAReapedFigaroIsNotListed(t *testing.T) {
	gone := fig("1", "gone-one", "", 9)
	gone.ReapedAt = 10
	if rows := Rows([]rpc.FigaroInfoResponse{gone, fig("2", "here-two", "", 1)}); len(rows) != 1 || rows[0].ID != "2" {
		t.Fatalf("rows = %+v, want only the live one", rows)
	}
}

// The form the pit reads and the rows the CLI prints are one encoding.
func TestASnapshotDecodesToItsRows(t *testing.T) {
	in := Rows([]rpc.FigaroInfoResponse{
		{ID: "1", Handle: "pushup-turtle", Mantra: "m", LastActive: 3, State: "active", Cwd: "/w"},
		fig("2", "ox-one", "", 2),
		fig("3", "", "", 0),
	})
	out := Decode(Snapshot(in))
	if len(out) != len(in) {
		t.Fatalf("decoded %d rows from %d", len(out), len(in))
	}
	for i := range in {
		if in[i] != out[i] {
			t.Errorf("row %d: encoded %+v, decoded %+v", i, in[i], out[i])
		}
	}
}
