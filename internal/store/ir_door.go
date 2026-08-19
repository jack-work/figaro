package store

import (
	"fmt"
	"sync"
	"time"

	"github.com/jack-work/figaro/internal/message"
)

// irDoor is the ONE write path into an aria's fig IR channel: every Append,
// from any provider or any caller, passes through it, and it is where the
// tool-call invariant is enforced rather than patrolled.
//
// THE INVARIANT: a message may not land while tool results are outstanding.
// If one tries, the door closes the open invokes with error results first --
// completing a partial result set in place, or synthesizing a whole one -- so
// the history a provider sees always pairs every invoke with a result.
//
// A result for a call that is no longer open does NOT fail the write. The
// block is dropped from the wire history (an unmatched tool_result is a hard
// error for every provider we speak) and a system note is appended in its
// place, so the model is told the result arrived rather than left to infer it.
//
// APPEND-ONLY: the door never edits a record already written. It completes the
// message it is given before writing it, and appends its own records after.
type irDoor struct {
	Log[message.Message]
	backend *XwalBackend
	ariaID  string

	mu      sync.Mutex
	pending []message.Content // outstanding invokes, oldest first
	loaded  bool
}

const (
	// toolClosedNotice is the result text for an invoke the door had to close.
	toolClosedNotice = "tool call closed without a result (interrupt, fork, or fault)"
	// lateResultNotice prefixes the system note for a result that arrived
	// after its call was closed.
	lateResultNotice = "a tool result arrived after its call was closed"
)

func (l *irDoor) Append(e Entry[message.Message]) (Entry[message.Message], error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.loaded {
		l.pending = outstandingInvokes(l.Log.Read())
		l.loaded = true
	}

	results, others := splitToolResults(e.Payload)
	var late []message.Content

	switch {
	case len(results) > 0:
		matched := map[string]bool{}
		for _, r := range results {
			if l.isPending(r.ToolCallID) {
				matched[r.ToolCallID] = true
			} else {
				late = append(late, r)
			}
		}
		// Drop the late blocks from the message: unmatched on the wire is a
		// hard error. They come back as a system note below.
		if len(late) > 0 {
			e.Payload.Content = keepContent(e.Payload.Content, func(c message.Content) bool {
				return c.Type != message.ContentToolResult || matched[c.ToolCallID]
			})
		}
		// Complete the set: anything still open is closed here, in this very
		// message, because a provider wants the results immediately after the
		// call and a second record would not be immediately after.
		for _, inv := range l.pending {
			if !matched[inv.ToolCallID] {
				e.Payload.Content = append(e.Payload.Content,
					message.ToolResultContent(inv.ToolCallID, inv.ToolName, toolClosedNotice, true))
			}
		}
		l.pending = nil

	case len(l.pending) > 0 && !message.IsCeremonial(e.Payload):
		// A message that is not the awaited results. Close the calls first.
		closing := message.Message{Role: message.RoleInput}
		for _, inv := range l.pending {
			closing.Content = append(closing.Content,
				message.ToolResultContent(inv.ToolCallID, inv.ToolName, toolClosedNotice, true))
		}
		l.pending = nil
		if _, err := l.write(Entry[message.Message]{Payload: closing}); err != nil {
			return Entry[message.Message]{}, err
		}
	}
	_ = others

	stamped, err := l.write(e)
	if err != nil {
		return stamped, err
	}

	// The message just written may itself open calls.
	if inv := assistantInvokes(e.Payload); len(inv) > 0 {
		l.pending = inv
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

// GuardIR wraps any fig IR log in the door, so an ephemeral aria gets the same
// tool-call invariant as a backed one. A backendless door skips the recency
// stamp and does nothing else differently.
func GuardIR(inner Log[message.Message]) Log[message.Message] {
	return &irDoor{Log: inner}
}

// CloseOpenToolCalls closes every outstanding invoke with an error result and
// reports how many it closed. The door does this on the next append anyway;
// calling it directly is for the INTERRUPT PATH, where the history must be
// well-formed the moment the turn ends rather than the next time somebody
// writes -- a fork or a read taken in between would otherwise see a call with
// no result.
func (l *irDoor) CloseOpenToolCalls() (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.loaded {
		l.pending = outstandingInvokes(l.Log.Read())
		l.loaded = true
	}
	if len(l.pending) == 0 {
		return 0, nil
	}
	closing := message.Message{Role: message.RoleInput}
	for _, inv := range l.pending {
		closing.Content = append(closing.Content,
			message.ToolResultContent(inv.ToolCallID, inv.ToolName, toolClosedNotice, true))
	}
	n := len(l.pending)
	l.pending = nil
	if _, err := l.write(Entry[message.Message]{Payload: closing}); err != nil {
		return 0, err
	}
	return n, nil
}

func (l *irDoor) write(e Entry[message.Message]) (Entry[message.Message], error) {
	stamped, err := l.Log.Append(e)
	if err == nil && l.backend != nil {
		l.backend.wroteTo(l.ariaID, time.Now().UnixMilli())
	}
	return stamped, err
}

func (l *irDoor) isPending(id string) bool {
	for _, c := range l.pending {
		if c.ToolCallID == id {
			return true
		}
	}
	return false
}

// outstandingInvokes reports the invokes of the last assistant message that
// still lack results, which is the door's state after a restart or a fork.
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
