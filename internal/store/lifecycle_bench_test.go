package store_test

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/jack-work/figaro/internal/message"
	"github.com/jack-work/figaro/internal/store"
)

// The everyday operations, at the layer that owns them: what `fig new`, `fig
// fork`, `fig kill` and `fig ls` actually do to the store. The form work moved
// all four (a birth writes a form patch, a fork inherits one, a kill closes a
// writer, a listing reads a published snapshot), so all four want numbers.

func birthPatch(i int) message.Patch {
	return message.Patch{Set: map[string]json.RawMessage{
		"system.provider": json.RawMessage(`"mock"`),
		"system.model":    json.RawMessage(`"m"`),
		"mantra":          json.RawMessage(fmt.Sprintf(`"aria-%d"`, i)),
	}}
}

// BenchmarkBirth is `fig new`: mint the outfit node, spawn under it, write the
// conversation's boot patch.
func BenchmarkBirth(b *testing.B) {
	back, err := store.NewXwalBackend(b.TempDir(), 0)
	if err != nil {
		b.Fatal(err)
	}
	defer back.Close()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		outfit, err := back.CreateOutfit("bench", birthPatch(i))
		if err != nil {
			b.Fatal(err)
		}
		id, err := back.CreateConversation(outfit)
		if err != nil {
			b.Fatal(err)
		}
		if _, err := back.ApplyForm(id, birthPatch(i)); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkFork is `fig fork`: branch at the head and dress the alternative,
// which is one form write on a node that came into existence a line earlier.
func BenchmarkFork(b *testing.B) {
	back, err := store.NewXwalBackend(b.TempDir(), 0)
	if err != nil {
		b.Fatal(err)
	}
	defer back.Close()
	outfit, err := back.CreateOutfit("bench", birthPatch(0))
	if err != nil {
		b.Fatal(err)
	}
	id, err := back.CreateConversation(outfit)
	if err != nil {
		b.Fatal(err)
	}
	if _, err := back.ApplyForm(id, birthPatch(0)); err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, alt, err := back.Fork(id)
		if err != nil {
			b.Fatal(err)
		}
		if _, err := back.ApplyForm(alt, message.Patch{
			Set: map[string]json.RawMessage{"aria_id": json.RawMessage(`"` + alt + `"`)},
		}); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkKill is `fig kill`: drop the handle, close the form's writer, remove
// the leaf, collect the stump it emptied.
func BenchmarkKill(b *testing.B) {
	back, err := store.NewXwalBackend(b.TempDir(), 0)
	if err != nil {
		b.Fatal(err)
	}
	defer back.Close()
	ids := make([]string, b.N)
	for i := range ids {
		outfit, err := back.CreateOutfit(fmt.Sprintf("bench-%d", i), birthPatch(i))
		if err != nil {
			b.Fatal(err)
		}
		id, err := back.CreateConversation(outfit)
		if err != nil {
			b.Fatal(err)
		}
		if _, err := back.ApplyForm(id, birthPatch(i)); err != nil {
			b.Fatal(err)
		}
		ids[i] = id
	}
	b.ResetTimer()
	for _, id := range ids {
		if err := back.Remove(id, false); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkListWithForms is `fig ls` as a client sees it: the topology walk plus
// the label read per row, which is what actually costs.
func BenchmarkListWithForms(b *testing.B) {
	for _, n := range []int{10, 100} {
		b.Run(fmt.Sprintf("arias=%d", n), func(b *testing.B) {
			back, err := store.NewXwalBackend(b.TempDir(), 0)
			if err != nil {
				b.Fatal(err)
			}
			defer back.Close()
			for i := 0; i < n; i++ {
				outfit, err := back.CreateOutfit(fmt.Sprintf("o-%d", i%4), birthPatch(i%4))
				if err != nil {
					b.Fatal(err)
				}
				id, err := back.CreateConversation(outfit)
				if err != nil {
					b.Fatal(err)
				}
				if _, err := back.ApplyForm(id, birthPatch(i)); err != nil {
					b.Fatal(err)
				}
			}
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if rows := back.Conversations(); len(rows) != n {
					b.Fatalf("rows = %d, want %d", len(rows), n)
				}
			}
		})
	}
}

// BenchmarkForkWith is the verb a human performs: `fig fork` as the angelus
// calls it. BenchmarkFork above measures Fork+ApplyForm, which is NOT this path
// — it never touches ForkWith — so it cannot be evidence about it.
func BenchmarkForkWith(b *testing.B) {
	back, err := store.NewXwalBackend(b.TempDir(), 0)
	if err != nil {
		b.Fatal(err)
	}
	defer back.Close()
	parent, _, err := back.ForkWith("", 0, birthPatch(0))
	if err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, _, err := back.ForkWith(parent, 0, birthPatch(i)); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkFormApplySameAria is the cost that is paid on EVERY form write —
// `fig set`, a mantra update, every system.* patch a turn commits — not once per
// fork. Folded inside a fork number it was invisible.
func BenchmarkFormApplySameAria(b *testing.B) {
	back, err := store.NewXwalBackend(b.TempDir(), 0)
	if err != nil {
		b.Fatal(err)
	}
	defer back.Close()
	outfit, err := back.CreateOutfit("bench", birthPatch(0))
	if err != nil {
		b.Fatal(err)
	}
	id, err := back.CreateConversation(outfit)
	if err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := back.ApplyForm(id, birthPatch(i)); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkFormApplyFreshAria is the same write against an aria nobody has
// touched: it includes OPENING the form, which replays the channel. A caller
// that only wants to append pays for materializing the whole state.
func BenchmarkFormApplyFreshAria(b *testing.B) {
	back, err := store.NewXwalBackend(b.TempDir(), 0)
	if err != nil {
		b.Fatal(err)
	}
	defer back.Close()
	outfit, err := back.CreateOutfit("bench", birthPatch(0))
	if err != nil {
		b.Fatal(err)
	}
	ids := make([]string, b.N)
	for i := range ids {
		id, cerr := back.CreateConversation(outfit)
		if cerr != nil {
			b.Fatal(cerr)
		}
		ids[i] = id
	}
	b.ResetTimer()
	for _, id := range ids {
		if _, err := back.ApplyForm(id, birthPatch(0)); err != nil {
			b.Fatal(err)
		}
	}
}
