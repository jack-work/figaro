package store

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/jack-work/figaro/internal/arialog"
)

// Issue 123: ReadFrom and TailSnapshot read through span, which cut the
// lineage by main-channel bases. On a cleared fork's translation channel that
// served the grandparent's rows where the node owns them.
func TestASpanOverAClearedChannelServesOwnRows(t *testing.T) {
	be, a, aLog := newConversation(t)
	aTrans, err := be.OpenTranslator(a, "anthropic")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10; i++ {
		ownRow(t, aTrans, put(t, aLog, ask(uint64(i+1), "a")), "a")
	}
	b, _, err := be.ForkWith(a, 0, birth("b"))
	if err != nil {
		t.Fatal(err)
	}
	bLog, _ := be.OpenFigIR(b)
	bTrans, _ := be.OpenTranslator(b, "anthropic")
	for i := 0; i < 5; i++ {
		ownRow(t, bTrans, put(t, bLog, ask(uint64(20+i), "b")), "b")
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

	own := func(label string, rows []arialog.Entry[[]json.RawMessage], n int) {
		t.Helper()
		if len(rows) != n {
			t.Fatalf("%s served %d rows, want %d", label, len(rows), n)
		}
		for _, r := range rows {
			if got := string(r.Payload[0]); got != fmt.Sprintf(`{"who":"c","at":%d}`, r.FigaroLT) {
				t.Fatalf("%s: row lt %d served %s, an ancestor's row where the node owns one", label, r.LT, got)
			}
		}
	}
	own("ReadFrom", cTrans.ReadFrom(1, 0), want)
	own("TailSnapshot", arialog.TailSnapshot[[]json.RawMessage](cTrans, want), want)
	own("TailSnapshot of 4", arialog.TailSnapshot[[]json.RawMessage](cTrans, 4), 4)
}
