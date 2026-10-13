package store

import (
	"fmt"
	"sync"
	"time"

	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/internal/turns"
)

// figIRLog is the one write path into an aria's IR channel, where the
// tool-call invariant is enforced: a message may not land while tool results
// are outstanding. The door closes open invokes with error results first,
// completing a partial result set in place or synthesizing a whole one.
//
// A result for a call no longer open does not fail the write: the block is
// dropped (an unmatched tool_result is a hard error for every provider) and a
// system note is appended in its place.
//
// Append-only: the door never edits a record already written.
type figIRLog struct {
	Log[message.Message]
	backend *XwalBackend
	ariaID  string

	// mu serializes this door's own appends; it does not make the door the
	// only writer. OpenFigIR mints one per call, so what is open is read from
	// the log on every append, never remembered.
	mu sync.Mutex
}

// roundLookback bounds the tail read that resolves outstanding invokes: every
// append through the door closes what it opened, so the only open invokes
// belong to the last round.
const roundLookback = 64

const (
	toolClosedNotice = "tool call closed without a result (interrupt, fork, or fault)"
	lateResultNotice = "a tool result arrived after its call was closed"
)

func (l *figIRLog) Append(e Entry[message.Message]) (Entry[message.Message], error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	pending := l.openInvokes()

	results, others := splitToolResults(e.Payload)
	var late []message.Content

	switch {
	case len(results) > 0:
		matched := map[string]bool{}
		for _, r := range results {
			if isOpen(pending, r.ToolCallID) {
				matched[r.ToolCallID] = true
			} else {
				late = append(late, r)
			}
		}
		// Late blocks leave the message (unmatched on the wire is a hard error)
		// and come back as a system note below.
		if len(late) > 0 {
			e.Payload.Content = keepContent(e.Payload.Content, func(c message.Content) bool {
				return c.Type != message.ContentToolResult || matched[c.ToolCallID]
			})
		}
		// Anything still open is closed in this very message: a provider wants
		// the results immediately after the call.
		for _, inv := range pending {
			if !matched[inv.ToolCallID] {
				e.Payload.Content = append(e.Payload.Content,
					message.ToolResultContent(inv.ToolCallID, inv.ToolName, toolClosedNotice, true))
			}
		}

	case len(pending) > 0 && !message.IsCeremonial(e.Payload):
		// A message that is not the awaited results. Close the calls first.
		closing := message.Message{Role: message.RoleInput}
		for _, inv := range pending {
			closing.Content = append(closing.Content,
				message.ToolResultContent(inv.ToolCallID, inv.ToolName, toolClosedNotice, true))
		}
		if _, err := l.write(Entry[message.Message]{Payload: closing}); err != nil {
			return Entry[message.Message]{}, err
		}
	}
	_ = others

	stamped, err := l.write(e)
	if err != nil {
		return stamped, err
	}

	for _, r := range late {
		note := message.Message{Role: message.RoleSystem, Content: []message.Content{
			message.TextContent(fmt.Sprintf("%s: tool %q (id %s) returned after the call was closed. Its output follows.\n\n%s",
				lateResultNotice, r.ToolName, r.ToolCallID, r.Text)),
		}}
		if _, err := l.write(Entry[message.Message]{Payload: note}); err != nil {
			return stamped, err
		}
	}
	return stamped, nil
}

// GuardIR wraps any IR log in the door so an ephemeral aria gets the same
// invariant; a backendless door only skips the recency stamp.
func GuardIR(inner Log[message.Message]) Log[message.Message] {
	return &figIRLog{Log: inner}
}

// CloseOpenToolCalls: the door does this on the next append anyway; the
// interrupt path calls it so a fork or read taken before the next write never
// sees a call without a result.
func (l *figIRLog) CloseOpenToolCalls() (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	pending := l.openInvokes()
	if len(pending) == 0 {
		return 0, nil
	}
	closing := message.Message{Role: message.RoleInput}
	for _, inv := range pending {
		closing.Content = append(closing.Content,
			message.ToolResultContent(inv.ToolCallID, inv.ToolName, toolClosedNotice, true))
	}
	n := len(pending)
	if _, err := l.write(Entry[message.Message]{Payload: closing}); err != nil {
		return 0, err
	}
	return n, nil
}

func (l *figIRLog) write(e Entry[message.Message]) (Entry[message.Message], error) {
	e.Payload.TurnID = l.turnIDFor(e.Payload)
	stamped, err := l.Log.Append(e)
	if err != nil || l.backend == nil {
		return stamped, err
	}
	l.backend.wroteTo(l.ariaID, time.Now().UnixMilli())
	// The IR entry is canonical and has landed; translations are derived and
	// follow. A torn translator frame is truncated at open, so a translation is
	// whole or absent, and an absent one is rebuilt by the next catch-up.
	l.backend.translateOnAppend(l.ariaID, l.Log, stamped)
	return stamped, nil
}

// openInvokes reads what is open from the log at every append, bounded to the
// round and never cached: a cached copy is a second answer that can disagree.
func (l *figIRLog) openInvokes() []message.Content {
	return outstandingInvokes(TailSnapshot(l.Log, roundLookback))
}

func isOpen(pending []message.Content, id string) bool {
	for _, c := range pending {
		if c.ToolCallID == id {
			return true
		}
	}
	return false
}

// outstandingInvokes: rows is a tail, not a history.
func outstandingInvokes(rows []Entry[message.Message]) []message.Content {
	at := -1
	for i := len(rows) - 1; i >= 0; i-- {
		if len(assistantInvokes(rows[i].Payload)) > 0 {
			at = i
			break
		}
	}
	if at < 0 {
		return nil
	}
	answered := map[string]bool{}
	for _, e := range rows[at+1:] {
		for _, c := range e.Payload.Content {
			if c.Type == message.ContentToolResult {
				answered[c.ToolCallID] = true
			}
		}
	}
	var open []message.Content
	for _, inv := range assistantInvokes(rows[at].Payload) {
		if !answered[inv.ToolCallID] {
			open = append(open, inv)
		}
	}
	return open
}

func assistantInvokes(m message.Message) []message.Content {
	if m.Role != message.RoleOutput {
		return nil
	}
	var out []message.Content
	for _, c := range m.Content {
		if c.Type == message.ContentToolInvoke {
			out = append(out, c)
		}
	}
	return out
}

func splitToolResults(m message.Message) (results, others []message.Content) {
	for _, c := range m.Content {
		if c.Type == message.ContentToolResult {
			results = append(results, c)
		} else {
			others = append(others, c)
		}
	}
	return results, others
}

func keepContent(in []message.Content, keep func(message.Content) bool) []message.Content {
	out := in[:0:0]
	for _, c := range in {
		if keep(c) {
			out = append(out, c)
		}
	}
	return out
}

func (b *XwalBackend) UnmatchedToolCalls(ariaID string) (int, error) {
	b.mu.Lock()
	h, err := b.handleLocked(ariaID)
	b.mu.Unlock()
	if err != nil {
		return 0, err
	}
	return len(unansweredInvokes(h.ir.Read())), nil
}

// duplicateResults: a second result for one invoke has nothing to pair with
// and the provider refuses the whole history. Entry LT and block, in log order.
func duplicateResults(rows []Entry[message.Message]) []struct {
	LT    uint64
	Block message.Content
} {
	seen := map[string]bool{}
	var out []struct {
		LT    uint64
		Block message.Content
	}
	for _, e := range rows {
		for _, c := range e.Payload.Content {
			if c.Type != message.ContentToolResult || c.ToolCallID == "" {
				continue
			}
			if seen[c.ToolCallID] {
				out = append(out, struct {
					LT    uint64
					Block message.Content
				}{e.LT, c})
				continue
			}
			seen[c.ToolCallID] = true
		}
	}
	return out
}

func unansweredInvokes(rows []Entry[message.Message]) []message.Content {
	answered := map[string]bool{}
	for _, e := range rows {
		for _, c := range e.Payload.Content {
			if c.Type == message.ContentToolResult {
				answered[c.ToolCallID] = true
			}
		}
	}
	var open []message.Content
	for _, e := range rows {
		for _, inv := range assistantInvokes(e.Payload) {
			if !answered[inv.ToolCallID] {
				open = append(open, inv)
			}
		}
	}
	return open
}

// RepairToolCalls is the one place that reads the whole history for unanswered
// invokes: the door keeps the invariant going forward, so this exists for
// records that predate it. Never on a read path.
func (b *XwalBackend) RepairToolCalls(ariaID string) (int, error) {
	b.mu.Lock()
	h, err := b.handleLocked(ariaID)
	b.mu.Unlock()
	if err != nil {
		return 0, err
	}
	open := unansweredInvokes(h.ir.Read())
	if len(open) == 0 {
		return 0, nil
	}
	closing := message.Message{Role: message.RoleInput}
	for _, inv := range open {
		closing.Content = append(closing.Content,
			message.ToolResultContent(inv.ToolCallID, inv.ToolName, toolClosedNotice, true))
	}
	guard := &figIRLog{Log: h.ir, backend: b, ariaID: ariaID}
	if _, err := guard.write(Entry[message.Message]{Payload: closing}); err != nil {
		return 0, err
	}
	return len(open), nil
}

// turnIDFor: no record reaches the log without a turn id. The rule is
// turns.StampIDs' own applied to one record, and the tail is the previous
// record. A caller-supplied id is kept: a steering message or a tool result
// belongs to the turn that provoked it.
func (l *figIRLog) turnIDFor(m message.Message) uint64 {
	if m.TurnID != 0 {
		return m.TurnID
	}
	var prev uint64
	if tail, ok := l.Log.PeekTail(); ok {
		prev = tail.Payload.TurnID
	}
	if turns.Opens(m) {
		return prev + 1
	}
	// Zero is not a failure to stamp: a record before the first prompt belongs
	// to no turn, and says so in its bytes.
	return prev
}
