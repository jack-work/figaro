package store

import (
	"encoding/json"
	"log/slog"
	"strings"
	"sync"

	"github.com/jack-work/figaro/internal/message"
)

// TranslatorEncoder turns ONE fig IR entry into the wire messages of ONE
// provider. It is injected: the store never learns what a provider is, and an
// encoder never learns what a log is.
//
// THE ENTRY IT IS HANDED IS ALREADY STAMPED AND ALREADY REPAIRED. It carries
// its LT, its form-channel version and its study versions, and its payload is
// the POST-REPAIR one -- the fig IR write path completes tool-result sets and
// drops unmatched blocks BEFORE a record lands, and that repaired payload
// exists nowhere else. That is the whole reason the encoding happens here
// rather than on a read.
type TranslatorEncoder interface {
	// Provider names the translator channel: "anthropic" writes to
	// translations-v2/anthropic.
	Provider() string
	// EncodeEntry returns the wire messages for this entry and the
	// fingerprint of the encoder that produced them. An entry that
	// translates to nothing returns none, and nothing is written.
	//
	// source IS THE FIG IR THE ENTRY CAME FROM. An encoder that keeps a
	// cursor needs to seed it from the entry a watermark names, which is an
	// EARLIER entry than this one -- so it is handed the log rather than
	// asked to find it.
	EncodeEntry(ariaID string, source Log[message.Message], e Entry[message.Message]) ([]json.RawMessage, string, error)
}

// translatorEncoders is the injected set, keyed by provider name. It is
// written once at wiring time and read on every append.
type translatorEncoders struct {
	mu sync.RWMutex
	by map[string]TranslatorEncoder
}

// add registers encoders WITHOUT dropping the ones already there. Providers
// are built one at a time -- one per aria, as arias open -- so a set that
// replaced would leave whichever provider registered last as the only one
// translating, and the others silently back on the catch-up.
func (t *translatorEncoders) add(encs []TranslatorEncoder) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.by == nil {
		t.by = map[string]TranslatorEncoder{}
	}
	for _, e := range encs {
		if e != nil && e.Provider() != "" {
			t.by[e.Provider()] = e
		}
	}
}

func (t *translatorEncoders) get(provider string) (TranslatorEncoder, bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	e, ok := t.by[provider]
	return e, ok
}

func (t *translatorEncoders) empty() bool {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return len(t.by) == 0
}

// AddTranslatorEncoders registers encoders the fig IR write path uses to
// translate each entry as it lands, keyed by provider. Wiring is the CLI's
// job, because that is where providers are built; a backend with none writes
// no translations and every provider catches up on its next send, which is
// the behaviour that predates this.
func (b *XwalBackend) AddTranslatorEncoders(encs ...TranslatorEncoder) {
	b.encoders.add(encs)
}

// translateOnAppend writes one translator entry per channel the aria ALREADY
// HAS, for the entry that just landed.
//
// THE FAN-OUT IS BY EXISTENCE, DECIDED BY CENSUS RATHER THAN BY ARGUMENT: on
// the author's real store, 726 arias hold 1.10 translator channels each
// against 1.03 that are live, with 22 fossils in the whole store, so a
// recency rule would buy 6% and cost a threshold. A channel an aria does not
// have is not created here: the first send through a new provider catches up,
// which is why 55% of arias -- the ones that have never sent -- cost nothing
// on this path.
//
// A FAILURE HERE DOES NOT FAIL THE APPEND. The fig IR entry is canonical and
// has landed; a translation that did not is derived, missing, and rebuilt by
// the next catch-up. That asymmetry is the whole reason the fig IR is written
// first.
func (b *XwalBackend) translateOnAppend(ariaID string, source Log[message.Message], e Entry[message.Message]) {
	if b == nil || b.encoders.empty() {
		return
	}
	// AN ASSISTANT ENTRY IS TRANSLATED BY THE PROVIDER THAT PRODUCED IT, NOT
	// HERE. Its wire form is the provider's OWN bytes -- thinking signatures,
	// encrypted reasoning -- which the fig IR deliberately does not hold, and
	// the turn commits them durably when the turn ends.
	//
	// A RENDERING WRITTEN HERE WOULD BE A SECOND ENTRY AT ONE FigaroLT, and
	// that is not a tie: the residency index is keyed by FigaroLT, a warm read
	// serves the FIRST entry and a cold read serves both
	// (TestTwoEntriesAtOneFigaroLTDivergeBetweenAWarmAndAColdRead). The
	// rendering lands first, so the model would be shown the UNSIGNED text
	// and the signed original would sit underneath it, unreachable until a
	// restart. Caught by scripts/live/onappendlive.sh, which read "4 5 5"
	// where the fig IR had 3 4 5.
	//
	// An assistant entry whose native commit never happens -- a repair, an
	// interrupted turn -- is translated by the next catch-up, exactly as
	// before.
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
			// Already translated -- a catch-up got here first. Writing again
			// would put two entries at one FigaroLT, which the residency
			// index cannot represent (see
			// TestTwoEntriesAtOneFigaroLTDivergeBetweenAWarmAndAColdRead).
			continue
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
