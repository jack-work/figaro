package store

import (
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/jack-work/figaro/api/message"
)

// TranslatorEncoder turns ONE fig IR entry into the wire messages of ONE
// provider. It is injected: the store never learns what a provider is, and an
// encoder never learns what a log is.
//
// The entry it is handed is already stamped and already repaired: its LT, its
// form-channel version and its study versions are set, and its payload is the
// post-repair one.
type TranslatorEncoder interface {
	// Provider names the translator channel: "anthropic" writes to
	// translations-v2/anthropic.
	Provider() string
	// EncodeEntry returns the wire messages for this entry and the
	// fingerprint of the encoder that produced them; an entry that
	// translates to nothing returns none. source is the fig IR the entry
	// came from, for an encoder that must seed a cursor from an earlier one.
	EncodeEntry(ariaID string, source Log[message.Message], e Entry[message.Message]) ([]json.RawMessage, string, error)
}

// translatorEncoders is the injected set, keyed by provider name.
//
// PUBLISHED, NOT LOCKED. It is written when a provider is built -- rarely,
// once per provider -- and read on EVERY fig IR append. A reader takes no
// lock: it loads one immutable map. Writers still exclude each other, because
// add is read-modify-write and there is more than one of them.
type translatorEncoders struct {
	writeMu sync.Mutex
	by      atomic.Pointer[map[string]TranslatorEncoder]
}

// add registers encoders WITHOUT dropping the ones already there: providers
// are built one at a time, as arias open. It builds a successor and publishes
// it, so a reader mid-flight keeps the map it loaded.
func (t *translatorEncoders) add(encs []TranslatorEncoder) {
	t.writeMu.Lock()
	defer t.writeMu.Unlock()
	next := map[string]TranslatorEncoder{}
	if cur := t.by.Load(); cur != nil {
		for k, v := range *cur {
			next[k] = v
		}
	}
	for _, e := range encs {
		if e != nil && e.Provider() != "" {
			next[e.Provider()] = e
		}
	}
	t.by.Store(&next)
}

func (t *translatorEncoders) get(provider string) (TranslatorEncoder, bool) {
	cur := t.by.Load()
	if cur == nil {
		return nil, false
	}
	e, ok := (*cur)[provider]
	return e, ok
}

func (t *translatorEncoders) empty() bool {
	cur := t.by.Load()
	return cur == nil || len(*cur) == 0
}

// AddTranslatorEncoders registers encoders the fig IR write path uses to
// translate each entry as it lands. A backend with none writes no
// translations, and every provider catches up on its next send.
func (b *XwalBackend) AddTranslatorEncoders(encs ...TranslatorEncoder) {
	b.encoders.add(encs)
}

// translateOnAppend writes one translator entry per channel the aria ALREADY
// HAS. A channel it does not have is not created here; the first send through
// a new provider catches up.
//
// A failure here does not fail the append: the fig IR entry is canonical, and
// a translation that did not land is derived, missing, and rebuilt by the next
// catch-up. Rationale and census: plans/delta-seam-rebased.md.
func (b *XwalBackend) translateOnAppend(ariaID string, source Log[message.Message], e Entry[message.Message]) {
	if b == nil || b.encoders.empty() {
		return
	}
	// An assistant entry is translated by the provider that produced it, from
	// its own wire bytes; one written here would be a second entry at one
	// FigaroLT. Asserted by TestAnAssistantEntryIsNotTranslatedOnAppend.
	if e.Payload.Role == message.RoleOutput {
		return
	}
	infos, err := b.store.trunks.Channels(ariaID)
	if err != nil {
		slog.Warn("translate on append: channels", "aria", ariaID, "err", err)
		return
	}
	for _, info := range infos {
		provider, ok := strings.CutPrefix(info.Name, translationPrefix)
		if !ok {
			continue
		}
		enc, ok := b.encoders.get(provider)
		if !ok {
			continue
		}
		trans, err := b.OpenTranslator(ariaID, provider)
		if err != nil {
			slog.Warn("translate on append: open", "aria", ariaID, "provider", provider, "err", err)
			continue
		}
		if tail, ok := trans.PeekTail(); ok && tail.FigaroLT >= e.LT {
			continue // a catch-up got here first
		}
		encoded, fingerprint, err := enc.EncodeEntry(ariaID, source, e)
		if err != nil {
			slog.Warn("translate on append: encode", "aria", ariaID, "provider", provider, "flt", e.LT, "err", err)
			continue
		}
		if len(encoded) == 0 {
			continue
		}
		hash, err := FigaroHash(e.Payload)
		if err != nil {
			slog.Warn("translate on append: hash", "aria", ariaID, "flt", e.LT, "err", err)
			continue
		}
		if _, err := trans.Append(Entry[[]json.RawMessage]{
			FigaroLT:    e.LT,
			Payload:     encoded,
			Fingerprint: fingerprint,
			FigaroHash:  hash,
		}); err != nil {
			slog.Warn("translate on append: write", "aria", ariaID, "provider", provider, "flt", e.LT, "err", err)
		}
	}
}
