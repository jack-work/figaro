package figaro

import (
	"github.com/jack-work/figaro/api/livedoc"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/internal/formdelta"
	"github.com/jack-work/figaro/internal/livelog/aria"
	"github.com/jack-work/figaro/internal/store"
)

// Projector converts fig IR into UI IR. It is the ONLY way the core reaches
// the projection, and it may be nil.
type Projector interface {
	// Turns projects sealed history into committed turns.
	Turns(msgs []message.Message) []aria.Turn

	// InquirySegments splits one opening message into its per-sender parts.
	// It goes through the projector for the same reason Turns does: the
	// fig IR -> UI IR conversion is the dependency this seam exists to keep
	// out of the engine, and deriving segments here rather than importing
	// compose is what keeps projector_boundary_test.go true.
	InquirySegments(m message.Message) []aria.InquirySegment

	// Nodes projects the open streaming region. tails carries the governor's
	// per-tool output tails, argPartials the still-truncated tool_use argument
	// JSON keyed by tool_call_id. The second return is the count of leading
	// nodes identical to those of the previous call, and the returned slice is
	// not mutated afterwards.
	Nodes(msgs []message.Message, tails, argPartials map[string]string) (prefix, suffix []livedoc.Node, stable int)

	// ResetTools clears per-turn tool timing state.
	ResetTools()
	// ToolOpened records when the model began WRITING a call; ToolStarted /
	// ToolFinished bracket when it RAN. Display only. The gap between opened
	// and started is generation, which for a large argument is nearly all of
	// the wall time.
	ToolOpened(id string, at int64)
	ToolStarted(id string, at int64)
	ToolFinished(id string, at int64)
}

// projTurns is the nil-safe form of Projector.Turns.
func (a *Agent) projTurns(msgs []message.Message) []aria.Turn {
	if a.proj == nil {
		return nil
	}
	return a.proj.Turns(msgs)
}

// projInquirySegments is the nil-safe form of Projector.InquirySegments.
func (a *Agent) projInquirySegments(m message.Message) []aria.InquirySegment {
	if a.proj == nil {
		return nil
	}
	return a.proj.InquirySegments(m)
}

// projNodes is the nil-safe form of Projector.Nodes.
func (a *Agent) projNodes(msgs []message.Message, tails, argPartials map[string]string) (prefix, suffix []livedoc.Node, stable int) {
	if a.proj == nil {
		return nil, nil, 0
	}
	return a.proj.Nodes(msgs, tails, argPartials)
}

// attachFormDeltas folds each record's form-state window onto sealed
// turns, exactly as the AriaReader does for a dormant aria. A LIVE aria is
// served by its agent, so without this the pager showed deltas only until
// the aria woke -- the same transcript telling two stories depending on
// liveness, which the purity invariant forbids.
func (a *Agent) attachFormDeltas(turns []aria.Turn, entries []store.Entry[message.Message]) []aria.Turn {
	fb, ok := a.backend.(formdelta.Backend)
	if !ok || len(turns) == 0 || len(entries) == 0 {
		return turns
	}
	formdelta.Attach(turns, formdelta.PerRecord(fb, a.id, entries))
	return turns
}

// attachFormDeltasFrom is attachFormDeltas for a SUFFIX of the entries: the
// per-record form cursor is seeded from the record before the suffix, exactly
// as turnSource does for a recomposed bracket. Without the seed the spliced
// turns would carry different deltas from the wholesale ones, which the
// equivalence oracle in seed_turns_test.go would catch.
func (a *Agent) attachFormDeltasFrom(turns []aria.Turn, entries []store.Entry[message.Message], at int) {
	fb, ok := a.backend.(formdelta.Backend)
	if !ok || len(turns) == 0 || at >= len(entries) {
		return
	}
	seed := formdelta.Seed{}
	if at > 0 {
		seed = formdelta.SeedFrom(entries[at-1])
	}
	formdelta.Attach(turns, formdelta.PerRecordFrom(fb, a.id, seed, entries[at:]))
}

// materializeTurns is the one walk behind every sealed-turn
// materialization: read the log once, compose, attach the form deltas.
func (a *Agent) materializeTurns() []aria.Turn {
	if a.figLog == nil {
		return nil
	}
	entries := a.figLog.Read()
	return a.attachFormDeltas(a.projTurns(unwrapMessages(entries)), entries)
}
