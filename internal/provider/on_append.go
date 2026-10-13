package provider

import (
	"encoding/json"
	"fmt"
	"sync"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/internal/store"
)

// OnAppend puts a provider's encoder behind store.TranslatorEncoder: one
// Deriver per aria, seeded from the translator log's watermark, the same
// derivation the catch-up uses. The accessors are functions of the aria
// because the adapter outlives any one aria.
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

// EncodeEntry returns an empty row for an entry that translates to nothing
// and no fingerprint for one that owes no row. The cursor is seeded from the
// translator's tail, never assumed, so a restart, fork or catch-up in between
// cannot render patches twice or not at all.
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
			watermark = SeedLT(trans)
		}
		// The watermark names an earlier entry, so seeding needs the log.
		d.SeedAt(source, watermark)
		o.at[ariaID] = d
	}
	msg, snap, translatable := d.Next(e)
	if !translatable {
		o.mu.Unlock()
		return nil, "", nil
	}
	encoded, err := o.encode(msg, snap)
	if err != nil {
		o.mu.Unlock()
		return nil, "", err
	}
	if len(encoded) == 0 {
		// An empty row commits no cursor: the window rides the next entry
		// that carries bytes.
		o.mu.Unlock()
		return []json.RawMessage{}, o.fingerprint(), nil
	}
	d.Commit(e, msg)
	o.mu.Unlock()
	return encoded, o.fingerprint(), nil
}

// Forget drops an aria's cursor so the next entry reseeds from the logs.
func (o *OnAppend) Forget(ariaID string) {
	o.mu.Lock()
	delete(o.at, ariaID)
	o.mu.Unlock()
}
