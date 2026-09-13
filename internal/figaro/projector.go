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

// materializeTurns is the one walk behind every sealed-turn
// materialization: read the log once, compose, attach the form deltas.
func (a *Agent) materializeTurns() []aria.Turn {
	if a.figLog == nil {
		return nil
	}
	entries := a.figLog.Read()
	return a.attachFormDeltas(a.projTurns(unwrapMessages(entries)), entries)
}

// stampSealDeltas writes the form state onto the turn about to be sealed.
//
// THE LIVE PATH COMPOSES FROM THE PROVIDER STREAM, which carries no form
// state at all, so a turn sealed in this process used to reach the pager
// with none: deltas appeared only for turns materialized at startup, and
// the same transcript told two stories depending on when you opened it.
//
// The window starts at the last record of the PREVIOUS turn that projected
// a node, not at this turn's first record. The records between the two
// turns -- a fork's birth record among them -- belong to the turn they
// opened, and beginning at the inquiry would step over them. See
// formdelta.Attach, which makes the same cut for a dormant aria.
func (a *Agent) stampSealDeltas() {
	fb, ok := a.backend.(formdelta.Backend)
	if !ok || a.figLog == nil || a.ariaSrv == nil {
		return
	}
	a.ariaSrv.StampTail(func(tail *aria.Turn, prev []aria.Turn) {
		deltas, ok := a.windowDeltas(fb, priorBoundary(prev))
		if !ok {
			return
		}
		// Every record in the window belongs to this turn: it is the last
		// one, so there is nothing after it to defer a seam to. AttachOne
		// says exactly that, and does not depend on the turn's LT range,
		// which the live composer has not stamped yet.
		formdelta.AttachOne(tail, deltas)
	})
}

// openingFormDeltas is the state a turn ENTERS with: the window from the
// previous turn's last node up to and including the record that opens this
// one. A fork's birth patch is in that window, and the banner it draws is
// how a reader walks back to the parent, so it has to be on the turn from
// the moment the question commits rather than at the seal an answer later.
func (a *Agent) openingFormDeltas() map[string]livedoc.FormDelta {
	fb, ok := a.backend.(formdelta.Backend)
	if !ok || a.figLog == nil || a.ariaSrv == nil {
		return nil
	}
	deltas, ok := a.windowDeltas(fb, a.ariaSrv.TailNodeLT())
	if !ok {
		return nil
	}
	var opening aria.Turn
	formdelta.AttachOne(&opening, deltas)
	return opening.FormDeltas
}

// windowDeltas derives the per-record deltas after the boundary record,
// seeded from that record's own cursors.
func (a *Agent) windowDeltas(fb formdelta.Backend, from uint64) (map[uint64]map[string]livedoc.FormDelta, bool) {
	entries, _ := a.figLog.ReadPage(from, 0, 0)
	if len(entries) == 0 {
		return nil, false
	}
	seed := formdelta.Seed{}
	if entries[0].LT == from {
		seed = formdelta.SeedFrom(entries[0])
		entries = entries[1:]
	}
	if len(entries) == 0 {
		return nil, false
	}
	return formdelta.PerRecordFrom(fb, a.id, seed, entries), true
}

// priorBoundary is the last record the previous turn drew, which is where
// this turn's window opens.
func priorBoundary(prev []aria.Turn) uint64 {
	if len(prev) == 0 {
		return 0
	}
	return aria.PriorBoundaryOf(prev[0])
}
