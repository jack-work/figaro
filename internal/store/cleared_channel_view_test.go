package store

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/jack-work/figaro/internal/arialog"
)

func ownRow(t *testing.T, l arialog.Log[[]json.RawMessage], figLT uint64, who string) {
	t.Helper()
	if _, err := l.Append(arialog.Entry[[]json.RawMessage]{
		FigaroLT: figLT,
		Payload:  []json.RawMessage{json.RawMessage(fmt.Sprintf(`{"who":%q,"at":%d}`, who, figLT))},
	}); err != nil {
		t.Fatal(err)
	}
}

// Issue 112 (quote-angelfish, bd43b2b7): an ancestor wrote anthropic rows, a
// descendant inherited some of them, and a later fork cleared its anthropic
// channel and rebuilt it, so its channel owns everything from 1 while the
// middle of the chain still carries a higher base. The view must serve the
// cleared node's own rows only, not refuse it forever.
func TestAClearedChannelShadowsEveryAncestor(t *testing.T) {
	be, a, aLog := newConversation(t)
	aTrans, err := be.OpenTranslator(a, "anthropic")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10; i++ {
		lt := put(t, aLog, ask(uint64(i+1), "a"))
		ownRow(t, aTrans, lt, "a")
	}
	b, _, err := be.ForkWith(a, 0, birth("b"))
	if err != nil {
		t.Fatal(err)
	}
	bLog, _ := be.OpenFigIR(b)
	bTrans, _ := be.OpenTranslator(b, "anthropic")
	for i := 0; i < 5; i++ {
		lt := put(t, bLog, ask(uint64(20+i), "b"))
		ownRow(t, bTrans, lt, "b")
	}
	c, _, err := be.ForkWith(b, 0, birth("c"))
	if err != nil {
		t.Fatal(err)
	}
	cLog, _ := be.OpenFigIR(c)
	for i := 0; i < 3; i++ {
		put(t, cLog, ask(uint64(30+i), "c"))
	}
	cTrans, _ := be.OpenTranslator(c, "anthropic")
	if err := cTrans.Clear(); err != nil {
		t.Fatal(err)
	}
	var want int
	for _, e := range cLog.Read() {
		ownRow(t, cTrans, e.LT, "c")
		want++
	}

	if arialog.ViewHidesOwnRows(cTrans) {
		t.Fatal("the cleared channel's view still serves an ancestor where the node owns rows")
	}
	rows := cTrans.Read()
	if len(rows) != want {
		t.Fatalf("view served %d rows, the node owns %d", len(rows), want)
	}
	for i, r := range rows {
		if got := string(r.Payload[0]); got != fmt.Sprintf(`{"who":"c","at":%d}`, r.FigaroLT) {
			t.Fatalf("row %d: served %s, an ancestor's row where the node owns one", i, got)
		}
	}
	var scanned int
	arialog.Scan(cTrans, 0, 0, func(r arialog.Entry[[]json.RawMessage]) bool {
		scanned++
		return true
	})
	if scanned != want {
		t.Fatalf("scan served %d rows, the node owns %d", scanned, want)
	}
}
