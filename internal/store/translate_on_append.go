package store

import (
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/jack-work/figaro/api/message"
)

// TranslatorEncoder turns one IR entry into one provider's wire messages. The
// entry it is handed is already stamped and repaired.
type TranslatorEncoder interface {
	Provider() string
	// EncodeEntry returns the wire messages and the encoder fingerprint; an
	// entry translating to nothing returns none. source lets an encoder seed a
	// cursor from an earlier entry.
	EncodeEntry(ariaID string, source Log[message.Message], e Entry[message.Message]) ([]json.RawMessage, string, error)
}

// translatorEncoders is published, not locked: read on every IR append,
// written once per provider. writeMu serializes add's read-modify-write.
type translatorEncoders struct {
	writeMu sync.Mutex
	by      atomic.Pointer[map[string]TranslatorEncoder]
}

// add publishes a successor map so a reader mid-flight keeps the one it loaded.
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

// A backend with no encoders writes no translations; every provider catches
// up on its next send.
func (b *XwalBackend) AddTranslatorEncoders(encs ...TranslatorEncoder) {
	b.encoders.add(encs)
}

// translateOnAppend writes one translator entry per channel the aria already
// has; a missing channel is created by the first send through that provider.
// A failure here never fails the append: the translation is derived and
// rebuilt by the next catch-up.
func (b *XwalBackend) translateOnAppend(ariaID string, source Log[message.Message], e Entry[message.Message]) {
	if b == nil || b.encoders.empty() {
		return
	}
	// An assistant entry is translated by the provider that produced it, from
	// its own wire bytes; one written here would be a second entry at one FigaroLT.
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
