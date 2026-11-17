package provider

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/internal/arialog"
)

type hidingLog[T any] struct{ arialog.Log[T] }

func (hidingLog[T]) HidesOwnRows() bool { return true }

// Catch-up over an IR view that serves an ancestor where the node owns
// records would encode another conversation and feed it to the model: it
// refuses, and writes nothing.
func TestCatchUpRefusesAnIRViewThatHidesOwnRows(t *testing.T) {
	f := openBatchFixture(t, t.TempDir(), 4096)
	f.newAria(t, 20)
	cfg := f.config(t, perRowChannel)
	cfg.Log = hidingLog[message.Message]{cfg.Log}
	if _, err := CatchUp(cfg); !errors.Is(err, arialog.ErrViewHidesOwnRows) {
		t.Fatalf("catch-up through a hiding IR view: err %v, want ErrViewHidesOwnRows", err)
	}
	if n := f.config(t, perRowChannel).Translator.Len(); n != 0 {
		t.Fatalf("the refused catch-up wrote %d rows", n)
	}
}

// Translator rows are derived: a translator view that hides the node's own
// rows is dropped and rebuilt from the fig IR, never left to fail every turn
// (issue 112).
func TestCatchUpRebuildsATranslatorViewThatHidesOwnRows(t *testing.T) {
	f := openBatchFixture(t, t.TempDir(), 4096)
	f.newAria(t, 20)
	if _, err := CatchUp(f.config(t, perRowChannel)); err != nil {
		t.Fatal(err)
	}
	want := rowDump(f.config(t, perRowChannel).Translator)

	f.appendRecord(t, 20)
	cfg := f.config(t, perRowChannel)
	cfg.Translator = hidingLog[[]json.RawMessage]{cfg.Translator}
	stats, err := CatchUp(cfg)
	if err != nil {
		t.Fatalf("catch-up through a hiding translator view: %v", err)
	}
	if stats.Visited != f.log.Len() {
		t.Fatalf("rebuilt from %d records, want all %d", stats.Visited, f.log.Len())
	}
	got := rowDump(f.config(t, perRowChannel).Translator)
	if len(got) != len(want)+1 {
		t.Fatalf("%d rows after the rebuild, want %d", len(got), len(want)+1)
	}
	sameRows(t, "rebuilt rows", got[:len(want)], want)
}
