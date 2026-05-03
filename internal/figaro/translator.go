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
	if a.translog == nil {
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
	if existing, ok := a.translog.Lookup(figaroLT); ok {
		if existing.Fingerprint == fp && rawMessagesEqual(existing.Payload, payload) {
			return
		}
	}
	if _, err := a.translog.Append(store.Entry[[]json.RawMessage]{
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
	if a.translog == nil || a.prov == nil {
		return
	}
	want := a.prov.Fingerprint()
	if want == "" {
		return
	}
	for _, e := range a.translog.Durable() {
		if e.Fingerprint == "" {
			continue
		}
		if e.Fingerprint != want {
			if err := a.translog.Clear(); err != nil {
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

// synchronize translates a freshly Recv'd event into the figaro
// events the act loop will dispatch. Pure translation — no fan-out.
// Notifications come from the act loop's switch on figaro events.
//
//   - eventTransLive: parse the live native chunk into a figaro
//     delta event (text + content type).
//   - eventSendComplete: condense the translog live tail using the
//     assembled native bytes from the summary, decode into IR, append
//     to figStream, emit one eventFigaro per assistant message, then
//     pass through the eventSendComplete so the loop can endTurn.
//   - everything else: pass through unchanged.
func (a *Agent) synchronize(raw event) []event {
	switch raw.typ {
	case eventTransLive:
		return decodeDelta(raw.transPayload)
	case eventSendComplete:
		out := a.condenseAndDecode(raw.sendAssistant, raw.sendSummary.Fingerprint)
		if raw.err == nil && a.translog != nil {
			a.persistProjectionSummary(unwrapMessages(a.figStream.Durable()), raw.sendSummary)
		}
		return append(out, raw)
	default:
		return []event{raw}
	}
}

// decodeDelta turns one translog live payload into a figaro delta
// event. Returns nil for empty / unparseable payloads.
func decodeDelta(payload []json.RawMessage) []event {
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
	return []event{{typ: eventFigaroDelta, deltaText: d.Delta, deltaCT: d.ContentType}}
}

// condenseAndDecode condenses the translog live tail into one durable
// entry using the assembled bytes Send returned, decodes into IR, and
// appends each assistant message to figStream. Returns one eventFigaro
// per appended message.
func (a *Agent) condenseAndDecode(assistant []json.RawMessage, fp string) []event {
	if a.translog == nil || len(assistant) == 0 {
		return nil
	}
	if _, err := a.translog.Condense(store.Entry[[]json.RawMessage]{
		Payload: assistant, Fingerprint: fp,
	}); err != nil {
		return nil
	}
	decoded, err := a.prov.Decode(assistant)
	if err != nil {
		return nil
	}
	var out []event
	for _, m := range decoded {
		if m.Role != message.RoleAssistant {
			continue
		}
		a.figStream.Append(store.Entry[message.Message]{Payload: m}, true)
		out = append(out, event{typ: eventFigaro, figMsg: m})
	}
	return out
}

// buildPriorTranslations returns a CausalSlice indexed in lockstep
// with msgs: index i holds the cached ProviderTranslation for
// msgs[i].LogicalTime when known, zero otherwise.
func (a *Agent) buildPriorTranslations(msgs []message.Message) causal.Slice[message.ProviderTranslation] {
	out := make([]message.ProviderTranslation, len(msgs))
	if a.translog != nil {
		for i, m := range msgs {
			if entry, ok := a.translog.Lookup(m.LogicalTime); ok {
				out[i] = message.ProviderTranslation{
					Messages: entry.Payload, Fingerprint: entry.Fingerprint,
				}
			}
		}
	}
	return causal.Wrap(out)
}
