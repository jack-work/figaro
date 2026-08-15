package figaro

import (
	"github.com/jack-work/figaro/internal/formdelta"
	"github.com/jack-work/figaro/internal/livedoc"
	"github.com/jack-work/figaro/internal/livelog/aria"
	"github.com/jack-work/figaro/internal/message"
	"github.com/jack-work/figaro/internal/store"
)

// Projector converts fig IR into UI IR. It is the ONLY way the core reaches
// the projection, and it may be nil.
//
// The core transacts in fig IR: it runs turns, appends message.Message to the
// xwal, mints turn ids, forks. None of that needs a rendering. A build that
// supplies no Projector still does all of it: it simply produces no UI frames
// and serves empty reads. That is the point: figaro-the-engine is usable
// without figaro-the-display, so a binary can ship without the conversion.
//
// The core still names the UI IR *shape* (aria.Turn, livedoc.Node) because it
// serves those types over the wire. What it no longer depends on is the
// *conversion*: internal/compose: which is the dependency that made the
// engine inseparable from the renderer. projector_boundary_test.go pins that.
//
// Tool timings live here rather than on the Agent because they exist only to
// be rendered; the engine never reads them back.
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
//
// The entries are the SAME read the turns were composed from: one walk,
// two consumers, and no moment between two reads for them to disagree in.
//
// The just-sealed turn's deltas appear when the server next materializes
// from the log (hydrate, reconcile, boot): a turn's stamps are only
// durable at its end, and the live stream never re-renders a sealed turn.
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

// turnSource is the agent's half of the turn cache's recompose-on-miss:
// read exactly the LT bracket, compose it, attach its deltas with the
// cursor seed from the record preceding the bracket. It reads through
// the SAME memoized log the whole agent uses, so a recompose costs
// decoded-window reads, not disk, when the range is warm below.
func (a *Agent) turnSource() aria.TurnSource {
	return func(fromLT, toLT uint64) []aria.Turn {
		log := a.figLog
		if log == nil || toLT < fromLT {
			return nil
		}
		entries, _ := log.ReadPage(fromLT, toLT+1, int(toLT-fromLT+1))
		if len(entries) == 0 {
			return nil
		}
		msgs := unwrapMessages(entries)
		turns := a.projTurns(msgs)
		if fb, ok := a.backend.(formdelta.Backend); ok {
			seed := formdelta.Seed{}
			if prev, _ := log.ReadPage(0, fromLT, 1); len(prev) > 0 {
				seed = formdelta.SeedFrom(prev[len(prev)-1])
			}
			formdelta.Attach(turns, formdelta.PerRecordFrom(fb, a.id, seed, entries))
		}
		return turns
	}
}
