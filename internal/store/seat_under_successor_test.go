package store

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/message"
)

type seatFixture struct {
	b                     *XwalBackend
	root, h0, h1, builder string
	rootTexts             []string
}

func said(text string) Entry[message.Message] {
	return Entry[message.Message]{Payload: message.Message{
		Role: message.RoleInput, Content: []message.Content{message.TextContent(text)},
	}}
}

func textsOf(rows []Entry[message.Message]) []string {
	var out []string
	for _, e := range rows {
		for _, c := range e.Payload.Content {
			if c.Text != "" {
				out = append(out, fmt.Sprintf("%d:%s", e.LT, c.Text))
			}
		}
	}
	return out
}

func mustSay(t *testing.T, b *XwalBackend, id string, texts ...string) []string {
	t.Helper()
	ir, err := b.OpenFigIR(id)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, text := range texts {
		e, err := ir.Append(said(text))
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, fmt.Sprintf("%d:%s", e.LT, text))
	}
	return out
}

func mustTranslate(t *testing.T, b *XwalBackend, id string) {
	t.Helper()
	ir, err := b.OpenFigIR(id)
	if err != nil {
		t.Fatal(err)
	}
	tr, err := b.OpenTranslator(id, "p")
	if err != nil {
		t.Fatal(err)
	}
	done := uint64(0)
	if tail, ok := tr.PeekTail(); ok {
		done = tail.FigaroLT
	}
	for _, e := range ir.ReadFrom(done+1, 0) {
		row := []json.RawMessage{json.RawMessage(fmt.Sprintf(`"row %d"`, e.LT))}
		if _, err := tr.Append(Entry[[]json.RawMessage]{FigaroLT: e.LT, Payload: row}); err != nil {
			t.Fatalf("translate %s at %d: %v", id, e.LT, err)
		}
	}
}

// The shape a succession line leaves: a first holder with a long history and
// successors forked off its tail, each with turns of its own. A builder forked
// from the last successor at an early coordinate is stored under the first
// holder, where the coordinate lives, and then seated under the successor.
func seatedBuilder(t *testing.T, keep int) seatFixture {
	t.Helper()
	b, err := NewXwalBackend(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { b.Close() })
	outfit, err := b.CreateOutfit("d", patchSet(map[string]string{"system.model": "m"}))
	if err != nil {
		t.Fatal(err)
	}
	f := seatFixture{b: b}
	if f.root, err = b.CreateConversation(outfit); err != nil {
		t.Fatal(err)
	}
	f.rootTexts = mustSay(t, b, f.root, "r1", "r2", "r3", "r4", "r5", "r6", "r7", "r8")
	mustTranslate(t, b, f.root)
	if _, f.h0, err = b.Fork(f.root); err != nil {
		t.Fatal(err)
	}
	mustSay(t, b, f.h0, "h0a", "h0b")
	mustTranslate(t, b, f.h0)
	if _, f.h1, err = b.Fork(f.h0); err != nil {
		t.Fatal(err)
	}
	mustSay(t, b, f.h1, "h1a", "h1b", "h1c")
	mustTranslate(t, b, f.h1)

	var at uint64
	if _, err := fmt.Sscanf(f.rootTexts[keep-1], "%d:", &at); err != nil {
		t.Fatal(err)
	}
	if _, f.builder, err = b.ForkAt(f.h1, at); err != nil {
		t.Fatal(err)
	}
	if n, _ := b.Node(f.builder); n.Parent != f.root {
		t.Fatalf("setup: the builder is stored under %s, want the first holder %s", n.Parent, f.root)
	}
	if err := b.Store().move(f.builder, f.h1); err != nil {
		t.Fatalf("seat the builder under the successor: %v", err)
	}
	if n, _ := b.Node(f.builder); n.Parent != f.h1 {
		t.Fatalf("setup: the builder sits under %s, want %s", n.Parent, f.h1)
	}
	return f
}

func irOf(t *testing.T, b *XwalBackend, id string) Log[message.Message] {
	t.Helper()
	ir, err := b.OpenFigIR(id)
	if err != nil {
		t.Fatal(err)
	}
	return ir
}

func scanned(log Log[message.Message]) []Entry[message.Message] {
	var out []Entry[message.Message]
	Scan(log, 0, 0, func(e Entry[message.Message]) bool { out = append(out, e); return true })
	return out
}

func TestASeatedBuilderReadsOnlyThePrefixItForkedWith(t *testing.T) {
	f := seatedBuilder(t, 2)
	want := f.rootTexts[:2]
	ir := irOf(t, f.b, f.builder)
	if got := textsOf(ir.Read()); !slices.Equal(got, want) {
		t.Fatalf("Read = %v, want %v", got, want)
	}
	if got := textsOf(ir.ReadFrom(0, 0)); !slices.Equal(got, want) {
		t.Fatalf("ReadFrom = %v, want %v", got, want)
	}
	if got := textsOf(scanned(ir)); !slices.Equal(got, want) {
		t.Fatalf("Scan = %v, want %v", got, want)
	}
	page, total := ir.ReadPage(0, 0, 100)
	if got := textsOf(page); !slices.Equal(got, want) {
		t.Fatalf("ReadPage = %v (of %d), want %v", got, total, want)
	}
}

func TestASeatedBuildersFirstWriteFollowsEverythingItReads(t *testing.T) {
	f := seatedBuilder(t, 2)
	inherited := irOf(t, f.b, f.builder).Read()
	last := inherited[len(inherited)-1].LT
	own := mustSay(t, f.b, f.builder, "mine 1", "mine 2", "mine 3")
	want := append(slices.Clone(f.rootTexts[:2]), own...)

	ir := irOf(t, f.b, f.builder)
	rows := ir.Read()
	if got := textsOf(rows); !slices.Equal(got, want) {
		t.Fatalf("Read after three writes = %v, want %v", got, want)
	}
	for i := 1; i < len(rows); i++ {
		if rows[i].LT <= rows[i-1].LT {
			t.Fatalf("LTs not increasing at %d: %d after %d", i, rows[i].LT, rows[i-1].LT)
		}
	}
	if first := rows[len(inherited)].LT; first != last+1 {
		t.Fatalf("the first write landed at LT %d, want %d: directly after the inherited prefix", first, last+1)
	}
	if got := textsOf(scanned(ir)); !slices.Equal(got, want) {
		t.Fatalf("Scan after three writes = %v, want %v", got, want)
	}
	if tail, ok := ir.PeekTail(); !ok || tail.Payload.Content[0].Text != "mine 3" {
		t.Fatalf("tail = %+v, want the builder's last write", tail)
	}
}

func TestASeatedBuilderTranslatesFromItsOwnFirstRecord(t *testing.T) {
	f := seatedBuilder(t, 2)
	tr, err := f.b.OpenTranslator(f.builder, "p")
	if err != nil {
		t.Fatal(err)
	}
	inherited := irOf(t, f.b, f.builder).Read()
	rows := tr.Read()
	if len(rows) != len(inherited) {
		t.Fatalf("the builder inherits %d translated rows for %d records", len(rows), len(inherited))
	}
	if tail, ok := tr.PeekTail(); !ok || tail.FigaroLT != inherited[len(inherited)-1].LT {
		t.Fatalf("translator tail = %+v, want the last inherited record", tail)
	}
	mustSay(t, f.b, f.builder, "mine 1", "mine 2")
	mustTranslate(t, f.b, f.builder)
	ir := irOf(t, f.b, f.builder).Read()
	rows = tr.Read()
	if len(rows) != len(ir) {
		t.Fatalf("%d translated rows for %d records", len(rows), len(ir))
	}
	for i := range rows {
		if rows[i].FigaroLT != ir[i].LT {
			t.Fatalf("row %d translates LT %d, want %d", i, rows[i].FigaroLT, ir[i].LT)
		}
	}
}

func TestSeatingABuilderLeavesTheSuccessorsReadsAlone(t *testing.T) {
	f := seatedBuilder(t, 2)
	mustSay(t, f.b, f.builder, "mine 1")
	got := textsOf(irOf(t, f.b, f.h1).Read())
	if len(got) != len(f.rootTexts)+5 || !slices.Equal(got[:len(f.rootTexts)], f.rootTexts) {
		t.Fatalf("the successor reads %v", got)
	}
	for i, want := range []string{"h0a", "h0b", "h1a", "h1b", "h1c"} {
		if text := got[len(f.rootTexts)+i]; !strings.HasSuffix(text, ":"+want) {
			t.Fatalf("the successor reads %q where it wrote %q", text, want)
		}
	}
}
