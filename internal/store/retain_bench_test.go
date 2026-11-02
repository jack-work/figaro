package store

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/jack-work/figaro/api/form"
)

// The mark must stay cheap on a big store: every form costs one directory
// listing, every other node only an index entry.
func BenchmarkRetainFormsMark(b *testing.B) {
	be, err := NewXwalBackend(b.TempDir(), 0)
	if err != nil {
		b.Fatal(err)
	}
	defer be.Close()
	outfit, err := be.CreateOutfit("base", form.Build(form.Snapshot{}, map[string]json.RawMessage{"skills.x": json.RawMessage(`1`)}, nil))
	if err != nil {
		b.Fatal(err)
	}
	for i := 0; i < 1000; i++ {
		if _, _, err := be.CreateForm("", form.Build(form.Snapshot{}, map[string]json.RawMessage{"n": json.RawMessage(`1`)}, nil)); err != nil {
			b.Fatal(err)
		}
	}
	for i := 0; i < 9000; i++ {
		if _, err := be.CreateConversation(outfit); err != nil {
			b.Fatal(err)
		}
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := be.RetainForms(context.Background(), true, 0); err != nil {
			b.Fatal(err)
		}
	}
}
