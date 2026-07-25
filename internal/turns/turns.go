// Package turns is the fig IR turn arithmetic: which message opens a turn,
// what id each message carries, and how a turn id maps to the LT range it
// occupies.
//
// It deliberately imports nothing but the fig IR. Turn identity is a property
// of the canonical record, not of any rendering of it, so the core can reason
// about turns — stamp ids, resolve a fork coordinate — without depending on
// the UI IR projection. This package previously lived inside internal/compose,
// which made every caller of the turn arithmetic drag in livedoc and the aria
// wire for no reason.
package turns

import (
	"strings"

	"github.com/jack-work/figaro/internal/message"
)

// Opens reports whether m begins a new turn. The rule is the one the
// projection has always applied implicitly: a user message carrying prose
// opens a turn, but a user message bearing tool_result stays inside the
// current turn even when it also carries text — that text is a steering
// interjection, not a new question.
func Opens(m message.Message) bool {
	return m.Role == message.RoleInput && !HasToolResult(m) && Text(m) != ""
}

// StampIDs fills TurnID on every message that lacks one and returns the last
// turn id the log implies — the seed an agent resumes minting from.
//
// It is a pure function of the slice: the same messages always yield the same
// ids, and nothing depends on whether the tail is still open. A message that
// already carries a TurnID resynchronises the counter, so a log with a legacy
// prefix and a stamped suffix agrees end to end.
//
// Fork seeding needs no machinery of its own. A child shares its parent's
// prefix verbatim, so counting turn openings over that prefix reproduces the
// parent's ids exactly and the child continues from there — siblings then
// conflict piecewise, which is precisely how LT already behaves.
//
// Messages before the first prompt (boot, state-only tics) belong to no turn
// and keep 0.
func StampIDs(msgs []message.Message) uint64 {
	var cur uint64
	for i := range msgs {
		if Opens(msgs[i]) {
			cur++
		}
		if id := msgs[i].TurnID; id != 0 {
			cur = id
			continue
		}
		msgs[i].TurnID = cur
	}
	return cur
}

// Span reports the LT range a turn occupies: first is the prompt's LT — the
// coordinate a fork takes — and last is the turn's final message.
//
// This is THE resolver. `fig send <aria>:<turn>` and `fig fork <aria>:<turn>`
// both route through it, and neither re-derives the walk: turn ids are
// ordinals, so resolving one to an LT means counting turn openings, which is
// exactly what StampIDs already does. Deriving it twice is the class of bug
// this whole change exists to kill.
//
// Fork semantics rest on first, but this function applies NO adjustment — it
// reports the honest span. atMainLT is INCLUSIVE of the frozen prefix (prefix
// [First,atMainLT], branch (atMainLT,Last]) — measured, not inferred: forking a
// real aria at atMainLT=5 yields a branch that still contains LT 5. So
// replacing turn T takes atMainLT = first - 1, and that -1 is fork POLICY that
// lives in exactly one place, cli.resolveTurn. Do not re-derive it here.
//
// Because that boundary is always a user prompt, the history a fork freezes
// always terminates on a complete assistant message — no tool_invoke is ever
// left dangling, so interrupted-tool synthesis is unreachable for a
// user-initiated fork.
func Span(msgs []message.Message, turn uint64) (first, last uint64, ok bool) {
	StampIDs(msgs)
	for i := range msgs {
		if msgs[i].TurnID != turn {
			continue
		}
		if !ok {
			first, ok = msgs[i].LogicalTime, true
		}
		last = msgs[i].LogicalTime
	}
	return first, last, ok
}

// At is the inverse of Span: it reports which turn owns an LT. Display
// surfaces need this because the forest records a fork point as an LT
// (NodeView.BranchedLT) while the user's coordinate is a turn — printing the
// raw LT and calling it a fork argument is a lie, since `fork <id>:N` takes N
// as a turn.
//
// BranchedLT is the branch's first own LT, which by the fork rule is exactly
// the prompt LT of the turn that was replaced, so At(msgs, BranchedLT) is the
// turn to name.
func At(msgs []message.Message, lt uint64) (uint64, bool) {
	StampIDs(msgs)
	for i := range msgs {
		if msgs[i].LogicalTime == lt {
			return msgs[i].TurnID, msgs[i].TurnID != 0
		}
	}
	return 0, false
}

// Text joins a message's prose blocks. Empty blocks are skipped here because
// this answers "does this message say anything", not "what does it render as".
func Text(m message.Message) string {
	var parts []string
	for _, c := range m.Content {
		if c.Type == message.ContentProse && strings.TrimSpace(c.Text) != "" {
			parts = append(parts, c.Text)
		}
	}
	return strings.Join(parts, "\n")
}

// HasToolResult reports whether a message carries any tool_result block — i.e.
// it is a tool-result tic (part of the turn) rather than a fresh user prompt.
func HasToolResult(m message.Message) bool {
	for _, c := range m.Content {
		if c.Type == message.ContentToolResult {
			return true
		}
	}
	return false
}
