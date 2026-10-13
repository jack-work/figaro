package provider

import (
	"encoding/json"
	"strconv"
	"testing"

	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/internal/store"
)

// A hole at position k costs N-k encodes and leaves every row before it
// byte-identical: the repair cuts from the gap, never from zero.
func TestAHoleIsRepairedFromTheGap(t *testing.T) {
	const n, k = 40, 25
	log := store.NewMemLog[message.Message]()
	for i := range n {
		appendProjectionMessage(t, log, "body "+strconv.Itoa(i))
	}
	full := store.NewMemLog[[]json.RawMessage]()
	if _, err := CatchUp(catchUpTestConfig(log, full)); err != nil {
		t.Fatal(err)
	}
	all := full.Read()
	missing := all[k-1].FigaroLT

	rows := store.NewMemLog[[]json.RawMessage]()
	for _, r := range all {
		if r.FigaroLT == missing {
			continue
		}
		if _, err := rows.Append(store.Entry[[]json.RawMessage]{
			FigaroLT: r.FigaroLT, Payload: r.Payload, Fingerprint: r.Fingerprint, FigaroHash: r.FigaroHash,
		}); err != nil {
			t.Fatal(err)
		}
	}
	before := rows.Read()
	var calls int
	stats, err := CatchUp(countingConfig(log, rows, &calls))
	if err != nil {
		t.Fatal(err)
	}
	if stats.RepairedAt != missing {
		t.Fatalf("RepairedAt=%d, want %d", stats.RepairedAt, missing)
	}
	if calls != n-k+1 {
		t.Fatalf("encoded %d rows, want %d: the rows from the gap on", calls, n-k+1)
	}
	after := rows.Read()
	if len(after) != n {
		t.Fatalf("rows=%d, want %d", len(after), n)
	}
	for i := range k - 1 {
		if after[i].LT != before[i].LT || after[i].FigaroLT != before[i].FigaroLT ||
			string(after[i].Payload[0]) != string(before[i].Payload[0]) {
			t.Fatalf("row %d below the gap was rewritten", i)
		}
	}
	for i, r := range after {
		if r.FigaroLT != all[i].FigaroLT || string(r.Payload[0]) != string(all[i].Payload[0]) {
			t.Fatalf("row %d: lt=%d payload=%s, want lt=%d %s", i, r.FigaroLT, r.Payload[0], all[i].FigaroLT, all[i].Payload[0])
		}
	}
	steady, err := CatchUp(countingConfig(log, &freshHandle{MemLog: rows}, &calls))
	if err != nil {
		t.Fatal(err)
	}
	if steady.RepairedAt != 0 || steady.Visited != 0 {
		t.Fatalf("after the repair: repaired=%d visited=%d", steady.RepairedAt, steady.Visited)
	}
}
