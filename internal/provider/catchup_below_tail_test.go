package provider

import (
	"encoding/json"
	"fmt"
	"strconv"
	"testing"

	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/internal/arialog"
)

// stuckCut reports a cut it never made, as xwal does for an absent channel
// or a cut past the last row, and refuses a row below the tail as xwal does.
type stuckCut struct{ arialog.Log[[]json.RawMessage] }

func (stuckCut) TruncateFrom(uint64) error { return nil }

func (s stuckCut) Append(e arialog.Entry[[]json.RawMessage]) (arialog.Entry[[]json.RawMessage], error) {
	if tail, ok := s.PeekTail(); ok && e.FigaroLT < tail.FigaroLT {
		return e, fmt.Errorf("main-LT must be non-decreasing: got %d, last %d", e.FigaroLT, tail.FigaroLT)
	}
	return s.Log.Append(e)
}

// Issue 90: the hole repair lowered the watermark below rows its cut left in
// place, and every later turn appended below the channel's tail. Whatever the
// cut did, catch-up must never write below the tail.
func TestCatchUpNeverWritesBelowTheTranslatorTail(t *testing.T) {
	const n, k = 40, 25
	log := arialog.NewMemLog[message.Message]()
	for i := range n {
		appendProjectionMessage(t, log, "body "+strconv.Itoa(i))
	}
	full := arialog.NewMemLog[[]json.RawMessage]()
	if _, err := CatchUp(catchUpTestConfig(log, full)); err != nil {
		t.Fatal(err)
	}
	all := full.Read()
	rows := arialog.NewMemLog[[]json.RawMessage]()
	for _, r := range all {
		if r.FigaroLT == all[k-1].FigaroLT {
			continue
		}
		if _, err := rows.Append(arialog.Entry[[]json.RawMessage]{
			FigaroLT: r.FigaroLT, Payload: r.Payload, Fingerprint: r.Fingerprint, FigaroHash: r.FigaroHash,
		}); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := CatchUp(catchUpTestConfig(log, stuckCut{rows})); err != nil {
		t.Fatalf("catch-up over a cut that did not happen: %v", err)
	}
	got := rows.Read()
	if len(got) != len(all) {
		t.Fatalf("%d rows after the repair, want %d", len(got), len(all))
	}
	for i, r := range got {
		if r.FigaroLT != all[i].FigaroLT || string(r.Payload[0]) != string(all[i].Payload[0]) {
			t.Fatalf("row %d: figaro lt %d, want %d", i, r.FigaroLT, all[i].FigaroLT)
		}
	}
}
