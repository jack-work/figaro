package figaro

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"

	"github.com/jack-work/figaro/internal/causal"
	"github.com/jack-work/figaro/internal/message"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/store"
)

// persistProjectionSummary writes the wire bytes the provider just
// projected for this Send into the per-aria translation stream, keyed
// by the figaro logical times each entry covers.
//
// Idempotent: an entry is skipped when the stream already has a
// matching entry for that FigaroLT with the same fingerprint and
// byte-equal Payload.
//
// State-only tics that emit no wire output have nil PerMessage[i] and
// are skipped (looking them up naturally misses). The bootstrap or
// rehydrate tic that establishes system.prompt gets the system block
// array entry instead, written below.
func (a *Agent) persistProjectionSummary(msgs []message.Message, summary provider.ProjectionSummary) {
	if a.transStream == nil {
		return
	}
	fp := summary.Fingerprint

	for i, msg := range msgs {
		var raw json.RawMessage
		if i < len(summary.PerMessage) {
			raw = summary.PerMessage[i]
		}
		if raw == nil {
			continue
		}
		if msg.LogicalTime == summary.SystemFLT && summary.SystemFLT != 0 {
			continue
		}
		a.maybeAppendTranslation(msg.LogicalTime, []json.RawMessage{raw}, fp)
	}

	if summary.SystemFLT != 0 && len(summary.System) > 0 {
		a.maybeAppendTranslation(summary.SystemFLT, summary.System, fp)
	}
}

func (a *Agent) maybeAppendTranslation(figaroLT uint64, payload []json.RawMessage, fp string) {
	if figaroLT == 0 {
		return
	}
	if existing, ok := a.transStream.Lookup(figaroLT); ok {
		if existing.Fingerprint == fp && rawMessagesEqual(existing.Payload, payload) {
			return
		}
	}
	if _, err := a.transStream.Append(store.Entry[[]json.RawMessage]{
		FigaroLT: figaroLT, Payload: payload, Fingerprint: fp,
	}, true); err != nil {
		fmt.Fprintf(os.Stderr, "figaro %s: translation stream append: %v\n", a.id, err)
	}
}

func rawMessagesEqual(a, b []json.RawMessage) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !bytes.Equal(a[i], b[i]) {
			return false
		}
	}
	return true
}

// invalidateTranslogIfStale clears the translation stream when any
// persisted entry's fingerprint disagrees with the provider's current
// Fingerprint(). The stream is a derivable cache.
func (a *Agent) invalidateTranslogIfStale() {
	if a.transStream == nil || a.prov == nil {
		return
	}
	want := a.prov.Fingerprint()
	if want == "" {
		return
	}
	for _, e := range a.transStream.Durable() {
		if e.Fingerprint == "" {
			continue
		}
		if e.Fingerprint != want {
			if err := a.transStream.Clear(); err != nil {
				fmt.Fprintf(os.Stderr, "figaro %s: translation stream clear: %v\n", a.id, err)
				return
			}
			fmt.Fprintf(os.Stderr,
				"figaro %s: cleared stale translation stream (fingerprint mismatch: stored=%q, current=%q)\n",
				a.id, e.Fingerprint, want)
			return
		}
	}
}

// synchronize is the translation orchestrator. After Recv, it applies
// any chalkboard input the event carries, brings the figaro stream
// even with the translation stream by decoding new entries, and on
// eventSendComplete writes the assembled assistant bytes through to
// the durable head. The act loop only ever sees figaro / control
// events.
//
// TODO: symmetric catchUpTranslation — when figStream is ahead of
// transStream, call Encode per-message and append. Requires splitting
// provider.Encode into per-message + request-assembly so Send can
// read pre-encoded bytes from the stream rather than encoding inline.
func (a *Agent) synchronize(raw event) []event {
	if raw.chalkboard != nil {
		a.applyChalkboardInput(raw.chalkboard)
	}
	if raw.typ == eventSendComplete && raw.err == nil && len(raw.sendAssistant) > 0 && a.transStream != nil {
		a.transStream.Condense(store.Entry[[]json.RawMessage]{
			Payload:     raw.sendAssistant,
			Fingerprint: raw.sendSummary.Fingerprint,
		})
		a.persistProjectionSummary(unwrapMessages(a.figStream.Durable()), raw.sendSummary)
	}
	out := a.catchUpFigaro()
	if raw.typ != eventTransLive {
		out = append(out, raw)
	}
	return out
}

// catchUpFigaro decodes any new translation entries (live + durable)
// into figaro events. Watermarks track the boundary; making this
// state-free would require backfilling FigaroLT on the translog entry
// after decode, which contradicts append-only semantics today.
func (a *Agent) catchUpFigaro() []event {
	if a.transStream == nil {
		return nil
	}
	var out []event
	for _, e := range a.transStream.Durable() {
		if e.LT <= a.lastDecodedTransLT {
			continue
		}
		a.lastDecodedTransLT = e.LT
		decoded, err := a.prov.Decode(e.Payload)
		if err != nil {
			continue
		}
		for _, m := range decoded {
			if m.Role != message.RoleAssistant {
				continue
			}
			a.figStream.Append(store.Entry[message.Message]{Payload: m}, true)
			out = append(out, event{typ: eventFigaro, figMsg: m})
		}
	}
	live := a.transStream.Live()
	if len(live) < a.lastDecodedLiveLen {
		a.lastDecodedLiveLen = 0
	}
	for i := a.lastDecodedLiveLen; i < len(live); i++ {
		if d := decodeDelta(live[i].Payload); d != nil {
			out = append(out, *d)
		}
	}
	a.lastDecodedLiveLen = len(live)
	return out
}

// decodeDelta parses one translog live payload into a figaro delta
// event. Nil for empty / unparseable payloads.
func decodeDelta(payload []json.RawMessage) *event {
	if len(payload) == 0 {
		return nil
	}
	var d struct {
		Delta       string              `json:"delta"`
		ContentType message.ContentType `json:"content_type,omitempty"`
	}
	if json.Unmarshal(payload[0], &d) != nil || d.Delta == "" {
		return nil
	}
	return &event{typ: eventFigaroDelta, deltaText: d.Delta, deltaCT: d.ContentType}
}

// buildPriorTranslations returns a CausalSlice indexed in lockstep
// with msgs: index i holds the cached ProviderTranslation for
// msgs[i].LogicalTime when known, zero otherwise.
func (a *Agent) buildPriorTranslations(msgs []message.Message) causal.Slice[message.ProviderTranslation] {
	out := make([]message.ProviderTranslation, len(msgs))
	if a.transStream != nil {
		for i, m := range msgs {
			if entry, ok := a.transStream.Lookup(m.LogicalTime); ok {
				out[i] = message.ProviderTranslation{
					Messages: entry.Payload, Fingerprint: entry.Fingerprint,
				}
			}
		}
	}
	return causal.Wrap(out)
}
