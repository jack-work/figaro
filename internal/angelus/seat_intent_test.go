package angelus

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/store"
)

type seatFixture struct {
	hs                    *Handlers
	b                     *store.XwalBackend
	top, pred, kid, other string
	successor, seatsFile  string
}

func newSeatFixture(t *testing.T) seatFixture {
	t.Helper()
	hs, b, outfit := demoteHandlers(t)
	f := seatFixture{hs: hs, b: b, seatsFile: filepath.Join(t.TempDir(), "seats.json")}
	var err error
	f.top, _ = b.CreateConversation(outfit)
	if _, f.pred, err = b.Fork(f.top); err != nil {
		t.Fatal(err)
	}
	if _, f.kid, err = b.Fork(f.pred); err != nil {
		t.Fatal(err)
	}
	f.other, _ = b.CreateConversation(outfit)
	if _, f.successor, err = b.Fork(f.other); err != nil {
		t.Fatal(err)
	}
	hs.h.seats = newSeats(f.seatsFile)
	return f
}

func (f seatFixture) drawn(id string) string {
	p, _ := f.b.HierarchyEngine().Drawn(id)
	return p
}

func (f seatFixture) overrides(t *testing.T) []byte {
	t.Helper()
	raw, err := json.Marshal(f.b.HierarchyEngine().Overrides())
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func (f seatFixture) leftRecords(t *testing.T) []seatRecord {
	t.Helper()
	raw, err := os.ReadFile(f.seatsFile)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var recs []seatRecord
	if err := json.Unmarshal(raw, &recs); err != nil {
		t.Fatal(err)
	}
	return recs
}

func (f seatFixture) seat(t *testing.T) rpc.SeatResponse {
	t.Helper()
	raw, _ := json.Marshal(rpc.SeatRequest{FigaroID: f.pred, Successor: f.successor})
	out, err := f.hs.Map[rpc.MethodSeat](inProcess(context.Background()), raw)
	if err != nil {
		t.Fatal(err)
	}
	return out.(rpc.SeatResponse)
}

// H1 review F1(a): the place lands and the demote is refused (the successor
// takes the predecessor's branch past its own max_children 0). The seat puts
// the successor back, so the drawn tree is what it was, byte for byte.
func TestASeatWhoseDemoteIsRefusedPutsTheSuccessorBack(t *testing.T) {
	f := newSeatFixture(t)
	setMaxChildren(t, f.b, f.successor, 0)
	before := f.overrides(t)

	seat := f.seat(t)
	if seat.FailedAt != "demote" || seat.Placed {
		t.Fatalf("seat = %+v, want a refused demote with the place undone", seat)
	}
	if got := f.drawn(f.successor); got != f.other {
		t.Fatalf("the successor is drawn under %q, want back under %s", got, f.other)
	}
	if after := f.overrides(t); !bytes.Equal(before, after) {
		t.Fatalf("drawn overrides changed: before %s, after %s", before, after)
	}
	if left := f.leftRecords(t); len(left) != 0 {
		t.Fatalf("seats.json still holds %+v", left)
	}
}

func TestASeatLeavesNoRecordOnceItLands(t *testing.T) {
	f := newSeatFixture(t)
	if seat := f.seat(t); seat.FailedAt != "" {
		t.Fatalf("seat failed at %s: %s", seat.FailedAt, seat.Error)
	}
	if got := f.drawn(f.pred); got != f.successor {
		t.Fatalf("the predecessor is drawn under %q, want the successor", got)
	}
	if left := f.leftRecords(t); len(left) != 0 {
		t.Fatalf("seats.json still holds %+v", left)
	}
}

// A stop between the place and the demote leaves the successor under its
// predecessor and the record on disk. The next daemon finishes the seat.
func TestASeatCutShortByAStopIsFinishedOnTheNextSweep(t *testing.T) {
	f := newSeatFixture(t)
	f.crashAfterThePlace(t)

	f.hs.h.finishSeats()
	if got := f.drawn(f.pred); got != f.successor {
		t.Fatalf("the predecessor is drawn under %q, want the successor", got)
	}
	if got := f.drawn(f.successor); got != f.top {
		t.Fatalf("the successor is drawn under %q, want the predecessor's place under %s", got, f.top)
	}
	if left := f.leftRecords(t); len(left) != 0 {
		t.Fatalf("seats.json still holds %+v", left)
	}
}

func TestASeatCutShortWhoseDemoteIsRefusedIsUndoneOnTheNextSweep(t *testing.T) {
	f := newSeatFixture(t)
	setMaxChildren(t, f.b, f.successor, 0)
	f.crashAfterThePlace(t)

	f.hs.h.finishSeats()
	if got := f.drawn(f.successor); got != f.other {
		t.Fatalf("the successor is drawn under %q, want back under %s", got, f.other)
	}
	if got := f.drawn(f.pred); got != f.top {
		t.Fatalf("the predecessor is drawn under %q, want where it was under %s", got, f.top)
	}
	if left := f.leftRecords(t); len(left) != 0 {
		t.Fatalf("seats.json still holds %+v", left)
	}
}

func (f *seatFixture) crashAfterThePlace(t *testing.T) {
	t.Helper()
	rec := f.hs.h.seatIntent(f.successor, f.pred)
	if err := f.hs.h.seats.begin(rec); err != nil {
		t.Fatal(err)
	}
	if err := f.b.HierarchyEngine().WriteDrawn(map[string]string{f.successor: f.pred}, nil); err != nil {
		t.Fatal(err)
	}
	f.hs.h.seats = newSeats(f.seatsFile)
	if left := f.leftRecords(t); len(left) != 1 {
		t.Fatalf("the crash left %d records, want 1", len(left))
	}
}
