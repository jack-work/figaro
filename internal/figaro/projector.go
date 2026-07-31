package figaro

import (
	"github.com/jack-work/figaro/internal/livedoc"
	"github.com/jack-work/figaro/internal/livelog/aria"
	"github.com/jack-work/figaro/internal/message"
)

// Projector converts fig IR into UI IR. It is the ONLY way the core reaches
// the projection, and it may be nil.
//
// The core transacts in fig IR: it runs turns, appends message.Message to the
// xwal, mints turn ids, forks. None of that needs a rendering. A build that
// supplies no Projector still does all of it — it simply produces no UI frames
// and serves empty reads. That is the point: figaro-the-engine is usable
// without figaro-the-display, so a binary can ship without the conversion.
//
// The core still names the UI IR *shape* (aria.Turn, livedoc.Node) because it
// serves those types over the wire. What it no longer depends on is the
// *conversion* — internal/compose — which is the dependency that made the
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
	// JSON keyed by tool_call_id.
	Nodes(msgs []message.Message, tails, argPartials map[string]string) []livedoc.Node

	// ResetTools clears per-turn tool timing state.
	ResetTools()
	// ToolStarted/ToolFinished record when a tool ran, for display only.
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
func (a *Agent) projNodes(msgs []message.Message, tails, argPartials map[string]string) []livedoc.Node {
	if a.proj == nil {
		return nil
	}
	return a.proj.Nodes(msgs, tails, argPartials)
}
