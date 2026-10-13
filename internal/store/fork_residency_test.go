package store

import (
	"testing"

	"github.com/jack-work/figaro/api/message"
)

// Measures where a fork's shared prefix is resident: if a fresh fork's window
// is mostly its parent's rows, sharing belongs at the fork base.
func TestWhereTheForkPrefixIsResident(t *testing.T) {
	b, err := NewXwalBackend(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()

	l, _ := b.CreateOutfit("d", patchSet(map[string]string{"system.model": "m"}))
	conv, _ := b.CreateConversation(l)

	ir, _ := b.OpenFigIR(conv)
	const history = 300
	for i := 0; i < history; i++ {
		if _, err := ir.Append(Entry[message.Message]{Payload: message.Message{
			Role:    message.RoleInput,
			Content: []message.Content{message.TextContent("a message of some length to make bytes real")},
		}}); err != nil {
			t.Fatal(err)
		}
	}

	// Fork near the tip: the fresh branch has almost no rows of its own.
	_, alt, err := b.ForkAt(conv, history-2)
	if err != nil {
		t.Fatal(err)
	}

	altIR, err := b.OpenFigIR(alt)
	if err != nil {
		t.Fatal(err)
	}
	rows := altIR.Read()
	if len(rows) == 0 {
		t.Fatal("the fork read nothing")
	}

	refs := b.Store().Lineage(alt)
	base := refs[len(refs)-1].Base

	var own, inherited int
	for _, e := range rows {
		if base > 0 && e.FigaroLT < base {
			inherited++
		} else {
			own++
		}
	}

	t.Logf("fork %s: base=%d, reads %d rows -- %d inherited from the parent, %d its own",
		alt, base, len(rows), inherited, own)

	if inherited == 0 {
		t.Fatal("the fork inherited nothing; the fixture cannot show duplication")
	}

	// A fresh fork's view is dominated by rows it does not own.
	if inherited <= own {
		t.Errorf("expected a fresh fork to be dominated by inherited rows, got %d inherited vs %d own",
			inherited, own)
	}
}
