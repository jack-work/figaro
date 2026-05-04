package figaro

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/jack-work/figaro/internal/chalkboard"
	"github.com/jack-work/figaro/internal/message"
	"github.com/jack-work/figaro/internal/store"
)

// invalidateTranslatorIfStale clears the translator on any
// Fingerprint mismatch. The stream is a derivable cache.
func (a *Agent) invalidateTranslatorIfStale() {
	if a.translator == nil || a.prov == nil {
		return
	}
	want := a.prov.Fingerprint()
	if want == "" {
		return
	}
	for _, e := range a.translator.Durable() {
		if e.Fingerprint == "" || e.Fingerprint == want {
			continue
		}
		if err := a.translator.Clear(); err != nil {
			fmt.Fprintf(os.Stderr, "figaro %s: translator clear: %v\n", a.id, err)
			return
		}
		fmt.Fprintf(os.Stderr,
			"figaro %s: cleared stale translator (fingerprint mismatch: stored=%q, current=%q)\n",
			a.id, e.Fingerprint, want)
		return
	}
}

// synchronize is the bidirectional translator orchestrator. Runs
// after every Recv: drains live deltas to UI events, on
// SendComplete folds the live tail into the durable head, then
// catches up the encode direction. The act loop only sees figaro /
// control events.
func (a *Agent) synchronize(raw event) []event {
	if raw.chalkboard != nil {
		a.applyChalkboardInput(raw.chalkboard)
	}

	out := a.catchUpFigaroDelta()
	if raw.typ == eventSendComplete {
		if raw.err == nil {
			out = append(out, a.condenseLive()...)
		} else if a.translator != nil {
			_ = a.translator.DiscardLive()
			a.lastDecodedLiveLen = 0
		}
	}
	a.catchUpTranslator()
	if raw.typ != eventTranslatorLive {
		out = append(out, raw)
	}
	return out
}

// condenseLive folds the live tail into one durable assistant
// entry: Assemble → Decode → figStream.Append → re-Encode for
// input-ready bytes → translator.Condense (FigaroLT links to the
// new figStream LT).
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

		// Re-encode strips inbound-only fields (stop_reason etc.) that
		// the API rejects on input. Cached bytes go in clean.
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
		fmt.Fprintf(os.Stderr, "figaro %s: condense: %v\n", a.id, err)
	}
	a.derived.Tick(lastFigaroLT, a.chalkboard.Snapshot())
	return out
}

// catchUpFigaroDelta decodes new live tail entries into UI deltas.
// Watermark resets on Condense.
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

// catchUpTranslator encodes any figStream entries that aren't yet
// in the translator. Idempotent.
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
			} else if _, err := a.translator.Append(store.Entry[[]json.RawMessage]{
				FigaroLT:    msg.LogicalTime,
				Payload:     payload,
				Fingerprint: fp,
			}, true); err != nil {
				fmt.Fprintf(os.Stderr, "figaro %s: translator append flt=%d: %v\n", a.id, msg.LogicalTime, err)
			}
		}
		for _, p := range msg.Patches {
			snap = snap.Apply(p)
		}
	}
}
