package figaro

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/jack-work/figaro/internal/chalkboard"
	"github.com/jack-work/figaro/internal/message"
	"github.com/jack-work/figaro/internal/store"
)

// invalidateTranslatorIfStale clears the translator stream when any
// persisted entry's fingerprint disagrees with the provider's current
// Fingerprint(). The stream is a derivable cache.
func (a *Agent) invalidateTranslatorIfStale() {
	if a.translator == nil || a.prov == nil {
		return
	}
	want := a.prov.Fingerprint()
	if want == "" {
		return
	}
	for _, e := range a.translator.Durable() {
		if e.Fingerprint == "" {
			continue
		}
		if e.Fingerprint != want {
			if err := a.translator.Clear(); err != nil {
				fmt.Fprintf(os.Stderr, "figaro %s: translator stream clear: %v\n", a.id, err)
				return
			}
			fmt.Fprintf(os.Stderr,
				"figaro %s: cleared stale translator stream (fingerprint mismatch: stored=%q, current=%q)\n",
				a.id, e.Fingerprint, want)
			return
		}
	}
}

// synchronize is the translation orchestrator that runs after every
// Recv. It applies any chalkboard input the event carries, decodes
// new live deltas into figaro UI events, on eventSendComplete folds
// the live tail into the durable head (Assemble → figStream append
// → translator Condense), and finally catches up the translator
// cache with any new figStream entries (encoding direction). The
// act loop only ever sees figaro / control events.
func (a *Agent) synchronize(raw event) []event {
	if raw.chalkboard != nil {
		a.applyChalkboardInput(raw.chalkboard)
	}

	var out []event
	// Decode first: drain any unread live deltas into figaro UI
	// events. condenseLive resets the live tail, so anything not
	// projected here is lost.
	out = append(out, a.catchUpFigaroDelta()...)
	if raw.typ == eventSendComplete {
		if raw.err == nil {
			out = append(out, a.condenseLive()...)
		} else if a.translator != nil {
			_ = a.translator.DiscardLive()
			a.lastDecodedLiveLen = 0
		}
	}
	// Encoding direction: fig → translator. Idempotent; cheap
	// (Lookup per figStream entry, encode misses only).
	a.catchUpTranslator()
	if raw.typ != eventTranslatorLive {
		out = append(out, raw)
	}
	return out
}

// condenseLive folds the translator live tail into the assembled
// assistant message: Assemble → Decode → figStream.Append → Condense
// (with FigaroLT linking the new translator entry to the freshly
// allocated figStream LT). Emits eventFigaro for any assistant
// message produced.
func (a *Agent) condenseLive() []event {
	if a.translator == nil {
		return nil
	}
	live := a.translator.Live()
	if len(live) == 0 {
		return nil
	}
	defer func() { a.lastDecodedLiveLen = 0 }()

	payloads := make([][]json.RawMessage, len(live))
	for i, e := range live {
		payloads[i] = e.Payload
	}
	assembled, err := a.prov.Assemble(payloads)
	if err != nil {
		fmt.Fprintf(os.Stderr, "figaro %s: assemble: %v\n", a.id, err)
		_ = a.translator.DiscardLive()
		return nil
	}
	if len(assembled) == 0 {
		_ = a.translator.DiscardLive()
		return nil
	}
	decoded, err := a.prov.Decode(assembled)
	if err != nil {
		fmt.Fprintf(os.Stderr, "figaro %s: decode assembled: %v\n", a.id, err)
		_ = a.translator.DiscardLive()
		return nil
	}

	var (
		out          []event
		lastFigaroLT uint64
		inputReady   []json.RawMessage
	)
	for _, m := range decoded {
		if m.Role != message.RoleAssistant {
			continue
		}
		entry, err := a.figStream.Append(store.Entry[message.Message]{Payload: m}, true)
		if err != nil {
			fmt.Fprintf(os.Stderr, "figaro %s: append assistant: %v\n", a.id, err)
			continue
		}
		lastFigaroLT = entry.LT
		out = append(out, event{typ: eventFigaro, figMsg: m})

		// Re-encode the IR message into input-ready bytes. The
		// inbound metadata (stop_reason, usage, model) lives on the
		// figaro Message; the translator queue only holds bytes the
		// provider can splice straight into the next request.
		encoded, err := a.prov.Encode(m, chalkboard.Snapshot{})
		if err != nil {
			fmt.Fprintf(os.Stderr, "figaro %s: re-encode assistant: %v\n", a.id, err)
			continue
		}
		inputReady = append(inputReady, encoded...)
	}
	if len(inputReady) == 0 {
		_ = a.translator.DiscardLive()
		return out
	}
	if _, err := a.translator.Condense(store.Entry[[]json.RawMessage]{
		FigaroLT:    lastFigaroLT,
		Payload:     inputReady,
		Fingerprint: a.prov.Fingerprint(),
	}); err != nil {
		fmt.Fprintf(os.Stderr, "figaro %s: condense translator: %v\n", a.id, err)
	}
	return out
}

// catchUpFigaroDelta projects new live tail entries (since the last
// scan) into figaro UI delta events. State-free across Condense
// because condenseLive resets the watermark.
func (a *Agent) catchUpFigaroDelta() []event {
	if a.translator == nil {
		return nil
	}
	live := a.translator.Live()
	if len(live) < a.lastDecodedLiveLen {
		a.lastDecodedLiveLen = 0
	}
	var out []event
	for i := a.lastDecodedLiveLen; i < len(live); i++ {
		decoded, err := a.prov.Decode(live[i].Payload)
		if err != nil {
			continue
		}
		for _, m := range decoded {
			for _, c := range m.Content {
				if c.Text == "" {
					continue
				}
				out = append(out, event{typ: eventFigaroDelta, deltaText: c.Text, deltaCT: c.Type})
			}
		}
	}
	a.lastDecodedLiveLen = len(live)
	return out
}

// catchUpTranslator walks the figaro stream and encodes any messages
// that lack a translator cache entry. Idempotent; safe to call
// before each Send. Empty payloads (state-only tics) are still
// stored so the lookup hits and we don't re-encode the next turn.
func (a *Agent) catchUpTranslator() {
	if a.translator == nil || a.prov == nil {
		return
	}
	fp := a.prov.Fingerprint()
	snap := chalkboard.Snapshot{}
	for _, msg := range unwrapMessages(a.figStream.Durable()) {
		if _, ok := a.translator.Lookup(msg.LogicalTime); !ok {
			payload, err := a.prov.Encode(msg, snap)
			if err != nil {
				fmt.Fprintf(os.Stderr, "figaro %s: encode flt=%d: %v\n", a.id, msg.LogicalTime, err)
			} else {
				if _, err := a.translator.Append(store.Entry[[]json.RawMessage]{
					FigaroLT:    msg.LogicalTime,
					Payload:     payload,
					Fingerprint: fp,
				}, true); err != nil {
					fmt.Fprintf(os.Stderr, "figaro %s: translator append flt=%d: %v\n", a.id, msg.LogicalTime, err)
				}
			}
		}
		for _, p := range msg.Patches {
			snap = snap.Apply(p)
		}
	}
}
