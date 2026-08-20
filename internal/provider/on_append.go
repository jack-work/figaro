package provider

import (
	"encoding/json"
	"fmt"
	"sync"

	"github.com/jack-work/figaro/internal/form"
	"github.com/jack-work/figaro/internal/message"
	"github.com/jack-work/figaro/internal/store"
)

// OnAppend puts a provider's encoder behind store.TranslatorEncoder, so a fig
// IR entry is translated by the site that holds its repaired payload rather
// than by a read that comes later.
//
// IT IS THE SAME DERIVATION THE CATCH-UP USES, one entry at a time: a Deriver
// per aria, seeded from the translator log's own watermark the first time it
// is needed and advanced as entries land. Two derivations would be two
// answers to "what did the board look like then", written to one channel.
//
// THE ACCESSORS ARE FUNCTIONS OF THE ARIA, not values, because a board and an
// observed set belong to an aria and this object outlives any one of them.
type OnAppend struct {
	name        string
	encode      func(message.Message, form.Snapshot) ([]json.RawMessage, error)
	fingerprint func() string
	board       func(ariaID string) Form
	studies     func(ariaID string) map[string]Form
	translator  func(ariaID string) (store.Log[[]json.RawMessage], error)

	mu sync.Mutex
	at map[string]*Deriver
}

// NewOnAppend builds the adapter. Every argument is a function of the aria
// because one adapter serves every aria this provider speaks for.
func NewOnAppend(
	name string,
	encode func(message.Message, form.Snapshot) ([]json.RawMessage, error),
	fingerprint func() string,
	board func(ariaID string) Form,
	studies func(ariaID string) map[string]Form,
	translator func(ariaID string) (store.Log[[]json.RawMessage], error),
) *OnAppend {
	return &OnAppend{
		name: name, encode: encode, fingerprint: fingerprint,
		board: board, studies: studies, translator: translator,
		at: map[string]*Deriver{},
	}
}

func (o *OnAppend) Provider() string { return o.name }

// EncodeEntry renders one entry. It returns no bytes for an entry that
// translates to nothing (genesis, an empty message), and the store writes
// nothing in that case.
//
// THE CURSOR IS SEEDED FROM THE LOGS, NEVER ASSUMED. The first entry after an
// open finds no cursor for that aria and rebuilds one from the translator's
// tail -- the same watermark a catch-up would use -- so a restart, a fork or
// a catch-up that ran in between cannot leave this path rendering patches
// twice or not at all.
func (o *OnAppend) EncodeEntry(ariaID string, source store.Log[message.Message], e store.Entry[message.Message]) ([]json.RawMessage, string, error) {
	o.mu.Lock()
	d, ok := o.at[ariaID]
	if !ok {
		d = NewDeriver(o.board(ariaID), o.studies(ariaID))
		var watermark uint64
		if o.translator != nil {
			trans, err := o.translator(ariaID)
			if err != nil {
				o.mu.Unlock()
				return nil, "", fmt.Errorf("seed %s cursor: %w", o.name, err)
			}
			if tail, ok := trans.PeekTail(); ok {
				watermark = tail.FigaroLT
			}
		}
		// SEEDING NEEDS THE FIG IR, and the entry that just landed is not it:
		// the watermark names an EARLIER entry. The caller holds the log, so
		// it is passed as the source of that one Lookup.
		d.SeedAt(source, watermark)
		o.at[ariaID] = d
	}
	msg, snap, translatable := d.Next(e)
	o.mu.Unlock()

	if !translatable {
		return nil, "", nil
	}
	encoded, err := o.encode(msg, snap)
	if err != nil {
		return nil, "", err
	}
	return encoded, o.fingerprint(), nil
}

// Forget drops an aria's cursor, so the next entry reseeds from the logs.
// Called when a fork or a clear makes the position meaningless.
func (o *OnAppend) Forget(ariaID string) {
	o.mu.Lock()
	delete(o.at, ariaID)
	o.mu.Unlock()
}
