package angelus

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jack-work/figaro/internal/form"
	"github.com/jack-work/figaro/internal/livelog/aria"
	"github.com/jack-work/figaro/internal/message"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/figaro/internal/tokens"
)

// AriaReader answers the read half of the aria contract — history, context
// and form — straight from the store, with no agent.
//
// It exists because reading was the last thing pinning an agent in memory.
// A transcript pager, a `figaro show`, a browser tab left open for a week:
// none of them need a turn loop, a provider or a tool registry, and none of
// them should keep 32 MB of composed UI resident to page through history.
//
// It is deliberately NOT a cache, and that is the whole memory argument: a
// hundred connected terminals do not pin a hundred transcripts. Each call
// rebuilds from the backend's ONE memoized log instance — the same instance
// EvictIdle drops — and drops the projection on return. Resident cost is
// per-aria and bounded; it does not scale with readers.
//
// The bill moves to churn, and it is not small (reader_bench_test.go):
//
//	Page      600 msgs   477 µs    916 KB/op
//	Page       10k msgs  4.63 ms  13.1 MB/op
//	Context    10k msgs   534 µs   2.8 MB/op   (decode only, no projection)
//	Form            257 ns     51 B/op
//
// So ~10 MB of the 13 is projection, thrown away each call: this is
// O(whole history) per page, not O(page). Against an agent's ~6 MB of
// permanently-resident composed UI at the same size, the trade is good for
// an aria read rarely and bad for one paged hard — which is exactly why
// handlers route a LIVE aria to its agent instead of here. A
// range-projecting reader that composes only the window would remove the
// asymmetry; it is the obvious next step and deliberately not this one.
//
// A LIVE agent must be preferred over this reader. The agent holds
// in-flight state — the open streaming region, partial tool arguments, the
// current turn — that is not in the store yet, and serving a stale page to
// a client watching a live turn would look like a hang. Callers route on
// liveness; see handlers.readerFor.
type AriaReader struct {
	backend store.Backend
	proj    Projector
}

// Projector converts fig IR into UI IR. Named here rather than imported
// from internal/figaro so the reader does not depend on the agent package:
// the whole point is that this path has no agent in it.
type Projector interface {
	Turns(msgs []message.Message) []aria.Turn
}

func NewAriaReader(backend store.Backend, proj Projector) *AriaReader {
	return &AriaReader{backend: backend, proj: proj}
}

// messages decodes the aria's IR. The backend hands back the same shared
// instance a live agent would hold, so this is lock-free against writes.
func (r *AriaReader) messages(id string) ([]message.Message, error) {
	if r == nil || r.backend == nil {
		return nil, errors.New("no backend (ephemeral angelus)")
	}
	if id == "" {
		return nil, errors.New("empty aria id")
	}
	log, err := r.backend.Open(id)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", id, err)
	}
	entries := log.Read()
	msgs := make([]message.Message, 0, len(entries))
	for _, e := range entries {
		msgs = append(msgs, e.Payload)
	}
	return msgs, nil
}

// Context returns the aria's fig IR plus the metrics a client renders
// beside it.
func (r *AriaReader) Context(id string) ([]message.Message, *aria.Metrics, error) {
	msgs, err := r.messages(id)
	if err != nil {
		return nil, nil, err
	}
	return msgs, r.metrics(id, msgs), nil
}

// Page projects sealed history and serves one window of it. at.Turn == 0
// with before means the tail. There is no open streaming region: a dormant
// aria has no turn in flight, which is what makes it dormant.
func (r *AriaReader) Page(id string, at aria.Anchor, budget int, before bool) (aria.Page, error) {
	msgs, err := r.messages(id)
	if err != nil {
		return aria.Page{}, err
	}
	srv := aria.NewServer()
	if r.proj != nil {
		for _, t := range r.proj.Turns(msgs) {
			srv.Commit(t)
		}
	}
	var page aria.Page
	if before {
		page = srv.ReadBefore(at, budget)
	} else {
		page = srv.Read(at, budget)
	}
	page.Metrics = r.metrics(id, msgs)
	return page, nil
}

// Form reads the reducible form channel, which is the durable
// truth — there is no the form channel to fall back on.
func (r *AriaReader) Form(id string) (form.Snapshot, error) {
	if r == nil || r.backend == nil {
		return form.Snapshot{}, errors.New("no backend (ephemeral angelus)")
	}
	snap, err := r.backend.FormState(id)
	if err != nil {
		return form.Snapshot{}, fmt.Errorf("form %s: %w", id, err)
	}
	return snap, nil
}

// metrics recomputes what the agent's refreshMetricsFrom computes, minus
// the fields that only a live turn can know. Token counts and context size
// are pure functions of the IR, so they round-trip exactly; a dormant aria
// simply has no provider bound, so the context LIMIT is whatever the
// form last recorded rather than a live lookup.
func (r *AriaReader) metrics(id string, msgs []message.Message) *aria.Metrics {
	contextTokens, exact := tokens.ContextSize(msgs)
	m := &aria.Metrics{ContextTokens: contextTokens, ContextExact: exact}
	for _, msg := range msgs {
		if u := msg.Usage; u != nil {
			m.TokensIn += u.InputTokens
			m.TokensOut += u.OutputTokens
			m.CacheReadTokens += u.CacheReadTokens
			m.CacheWriteTokens += u.CacheWriteTokens
		}
	}
	if snap, err := r.Form(id); err == nil {
		m.Mantra = snapString(snap, "mantra")
	}
	return m
}

func snapString(snap form.Snapshot, key string) string {
	raw, ok := snap.Get(key)
	if !ok {
		return ""
	}
	var v string
	_ = json.Unmarshal(raw, &v)
	return v
}
