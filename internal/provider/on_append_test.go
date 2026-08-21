package provider

import (
	"encoding/json"
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/internal/store"
)

// THE TWO PATHS MUST AGREE, BECAUSE THEY WRITE TO ONE CHANNEL.
//
// A catch-up walks a suffix; the fig IR write path renders each entry as it
// lands. If they derived the board differently, an aria's history would read
// one way when it was written live and another when it was rebuilt -- and
// nothing downstream could tell which it had.
//
// This drives the SAME log through both and compares the bytes.
func TestOnAppendRendersWhatACatchUpWould(t *testing.T) {
	board := newFakeBoard(2, 3, 4, 5, 6)
	build := func() *store.MemLog[message.Message] {
		log := store.NewMemLog[message.Message]()
		stamp(t, log, "one", 2)
		stamp(t, log, "two", 4)
		stamp(t, log, "three", 6)
		return log
	}
	encode := func(msg message.Message, snap form.Snapshot) ([]json.RawMessage, error) {
		keys := []string{}
		for _, p := range msg.Patches {
			for k := range p.Set {
				keys = append(keys, k)
			}
		}
		body, err := json.Marshal(map[string]any{
			"text":    msg.Content[0].Text,
			"patches": keys,
			"board":   snap.Len(),
		})
		if err != nil {
			return nil, err
		}
		return []json.RawMessage{body}, nil
	}

	// ARM A: one catch-up over the whole log.
	catchLog, catchTrans := build(), store.NewMemLog[[]json.RawMessage]()
	if _, err := CatchUp(CatchUpConfig{
		Log: catchLog, Translator: catchTrans, Form: board,
		Fingerprint: "fp/v1", Encode: encode,
	}); err != nil {
		t.Fatal(err)
	}

	// ARM B: the write path, entry by entry, through the adapter.
	appendLog, appendTrans := build(), store.NewMemLog[[]json.RawMessage]()
	adapter := NewOnAppend("fake",
		encode,
		func() string { return "fp/v1" },
		func(string) Form { return newFakeBoard(2, 3, 4, 5, 6) },
		func(string) map[string]Form { return nil },
		func(string) (store.Log[[]json.RawMessage], error) { return appendTrans, nil },
	)
	for _, e := range appendLog.Read() {
		encoded, fp, err := adapter.EncodeEntry("aria", appendLog, e)
		if err != nil {
			t.Fatal(err)
		}
		if len(encoded) == 0 {
			continue
		}
		if _, err := appendTrans.Append(store.Entry[[]json.RawMessage]{
			FigaroLT: e.LT, Payload: encoded, Fingerprint: fp,
		}); err != nil {
			t.Fatal(err)
		}
	}

	want, got := catchTrans.Read(), appendTrans.Read()
	if len(want) != len(got) {
		t.Fatalf("catch-up wrote %d entries, the write path wrote %d", len(want), len(got))
	}
	for i := range want {
		if want[i].FigaroLT != got[i].FigaroLT {
			t.Fatalf("entry %d: catch-up names FigaroLT %d, write path %d", i, want[i].FigaroLT, got[i].FigaroLT)
		}
		if string(want[i].Payload[0]) != string(got[i].Payload[0]) {
			t.Fatalf("entry %d differs:\n catch-up  %s\n on-append %s",
				i, want[i].Payload[0], got[i].Payload[0])
		}
	}
}

// AND THE ADAPTER RESUMES FROM THE LOG, NOT FROM MEMORY. A cursor that did not
// survive a restart would render the whole board onto the first entry after
// it -- the exact defect the deleted memo had, one layer over.
func TestOnAppendSeedsItsCursorFromTheTranslatorTail(t *testing.T) {
	log := store.NewMemLog[message.Message]()
	trans := store.NewMemLog[[]json.RawMessage]()
	stamp(t, log, "one", 2)
	stamp(t, log, "two", 4)

	var rendered [][]string
	encode := func(msg message.Message, _ form.Snapshot) ([]json.RawMessage, error) {
		keys := []string{}
		for _, p := range msg.Patches {
			for k := range p.Set {
				keys = append(keys, k)
			}
		}
		rendered = append(rendered, keys)
		return []json.RawMessage{json.RawMessage(`"x"`)}, nil
	}
	newAdapter := func() *OnAppend {
		return NewOnAppend("fake", encode, func() string { return "fp/v1" },
			func(string) Form { return newFakeBoard(2, 3, 4, 5, 6) },
			func(string) map[string]Form { return nil },
			func(string) (store.Log[[]json.RawMessage], error) { return trans, nil })
	}

	entries := log.Read()
	// One adapter renders the first entry and is then thrown away, as a
	// restart throws away everything held in memory.
	first, fp, err := newAdapter().EncodeEntry("aria", log, entries[0])
	if err != nil {
		t.Fatal(err)
	}
	if _, err := trans.Append(store.Entry[[]json.RawMessage]{
		FigaroLT: entries[0].LT, Payload: first, Fingerprint: fp,
	}); err != nil {
		t.Fatal(err)
	}

	// A FRESH adapter renders the second. If it seeded from zero it would
	// re-render the patches the first entry already carried.
	if _, _, err := newAdapter().EncodeEntry("aria", log, entries[1]); err != nil {
		t.Fatal(err)
	}

	if len(rendered) != 2 {
		t.Fatalf("rendered %d entries, want 2", len(rendered))
	}
	for _, k := range rendered[1] {
		for _, first := range rendered[0] {
			if k == first {
				t.Fatalf("the second entry re-rendered %q, which the first already carried: "+
					"the cursor did not resume from the translator tail", k)
			}
		}
	}
	if len(rendered[1]) == 0 {
		t.Fatal("the second entry rendered no patches at all, so this proves nothing")
	}
}
