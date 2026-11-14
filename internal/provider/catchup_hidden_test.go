package provider

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/internal/store"
)

type hidingLog[T any] struct{ store.Log[T] }

func (hidingLog[T]) HidesOwnRows() bool { return true }

// Catch-up over a view that serves an ancestor where the node owns records
// would encode another conversation and feed it to the model: it refuses, and
// writes nothing.
func TestCatchUpRefusesAViewThatHidesOwnRows(t *testing.T) {
	f := openBatchFixture(t, t.TempDir(), 4096)
	f.newAria(t, 20)
	for _, wrap := range []func(*CatchUpConfig){
		func(c *CatchUpConfig) { c.Log = hidingLog[message.Message]{c.Log} },
		func(c *CatchUpConfig) { c.Translator = hidingLog[[]json.RawMessage]{c.Translator} },
	} {
		cfg := f.config(t, perRowChannel)
		wrap(&cfg)
		if _, err := CatchUp(cfg); !errors.Is(err, store.ErrViewHidesOwnRows) {
			t.Fatalf("catch-up through a hiding view: err %v, want ErrViewHidesOwnRows", err)
		}
		if n := f.config(t, perRowChannel).Translator.Len(); n != 0 {
			t.Fatalf("the refused catch-up wrote %d rows", n)
		}
	}
}
