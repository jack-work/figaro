package store

import (
	"testing"

	"github.com/jack-work/figaro/api/message"
)

// WHERE IS THE DUPLICATION, actually?
//
// The ruling says the decoded IR joins the tree because "two forks of one
// trunk each retain the shared prefix separately". This measures where that
// shared prefix is RESIDENT, because it decides what the re-seat must share.
//
// If the duplication is below the window, the fix is at the window edge and
// the cache backs the fall-through. If a fork's resident window is itself
// mostly its parent's rows, the fix is at the FORK BASE and the fall-through
// is beside the point.
//
// PERMANENT, NOT MIGRATION SCAFFOLDING.
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

	// The finding, asserted so it cannot quietly stop being true: a fresh
	// fork's view is dominated by rows it does not own, and both trunks
	// decode them separately today.
	if inherited <= own {
		t.Errorf("expected a fresh fork to be dominated by inherited rows, got %d inherited vs %d own",
			inherited, own)
	}
}
