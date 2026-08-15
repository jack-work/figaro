package compose

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/jack-work/figaro/internal/livedoc"
	"github.com/jack-work/figaro/internal/message"
)

// THE HAZARD, pinned before the composer that could violate it.
//
// Incremental composition must produce EXACTLY what wholesale composition
// produces, for the same inputs, AT EVERY FRAME -- not merely at seal. A
// composer that converges only when the turn seals shows correct transcripts
// and wrong live frames, and live/committed divergence is forbidden by
// invariant. So the test drives a whole turn frame by frame, the way the agent
// does, and compares node for node on every single frame.
//
// The frame script is built to contain the ways a node MUTATES after it was
// first composed: a tool_invoke composed while its result is still absent, the
// result landing a message later, argument JSON arriving in pieces, output
// streaming under a running tool, timings stamped after the node existed, a
// steering interjection arriving mid-turn, and an interrupt repair appending
// an aborted assistant message.

// frame is one call the agent would make to the projector.
type frame struct {
	what        string
	msgs        []message.Message
	partials    map[string]string
	argPartials map[string]string
	timings     map[string]ToolTiming
}

// turnScript builds the frame sequence for a turn of r tool rounds. Every
// frame carries the WHOLE open region, exactly as composeTurn hands it over.
func turnScript(r int) []frame {
	var frames []frame
	var durable []message.Message
	partials := map[string]string{}
	argPartials := map[string]string{}
	timings := map[string]ToolTiming{}
	lt := uint64(1)

	snap := func(what string, inflight *message.Message) {
		msgs := append([]message.Message(nil), durable...)
		if inflight != nil {
			m := *inflight
			m.LogicalTime = lt + 1
			msgs = append(msgs, m)
		}
		f := frame{what: what, msgs: msgs,
			partials:    map[string]string{},
			argPartials: map[string]string{},
			timings:     map[string]ToolTiming{},
		}
		for k, v := range partials {
			f.partials[k] = v
		}
		for k, v := range argPartials {
			f.argPartials[k] = v
		}
		for k, v := range timings {
			f.timings[k] = v
		}
		frames = append(frames, f)
	}
	appendDurable := func(m message.Message) {
		lt++
		m.LogicalTime = lt
		durable = append(durable, m)
	}

	// The user's prompt opens the region.
	appendDurable(message.Message{Role: message.RoleInput,
		Content: []message.Content{message.TextContent("do the thing")}})
	snap("prompt", nil)

	for round := 0; round < r; round++ {
		id := fmt.Sprintf("call_%d", round)

		// The assistant streams prose into a message that is not yet durable.
		inflight := &message.Message{Role: message.RoleOutput}
		for i := 1; i <= 3; i++ {
			inflight.Content = []message.Content{
				{Type: message.ContentProse, Text: strings.Repeat("thinking out loud. ", i)},
			}
			snap(fmt.Sprintf("round %d: prose frame %d", round, i), inflight)
		}
		// A tool block opens: the arguments arrive as raw JSON in pieces, and
		// the node exists before they finish.
		inflight.Content = append(inflight.Content,
			message.Content{Type: message.ContentToolInvoke, ToolCallID: id, ToolName: "bash"})
		for i, part := range []string{`{"comm`, `{"command": "ls -`, `{"command": "ls -la"}`} {
			argPartials[id] = part
			timings[id] = ToolTiming{OpenedAt: 100 + int64(i)}
			snap(fmt.Sprintf("round %d: args frame %d", round, i), inflight)
		}
		// The assistant message becomes durable, arguments decoded.
		delete(argPartials, id)
		appendDurable(message.Message{Role: message.RoleOutput, Content: []message.Content{
			{Type: message.ContentProse, Text: strings.Repeat("thinking out loud. ", 3)},
			{Type: message.ContentToolInvoke, ToolCallID: id, ToolName: "bash",
				Arguments: map[string]any{"command": "ls -la"}},
		}})
		t := timings[id]
		t.StartedAt = 200
		timings[id] = t
		snap(fmt.Sprintf("round %d: assistant durable", round), nil)

		// The tool runs: output streams under a node whose result is absent.
		for i := 1; i <= 3; i++ {
			partials[id] = strings.Repeat("streamed output line\n", i*90)
			snap(fmt.Sprintf("round %d: output frame %d", round, i), nil)
		}
		// A steering interjection arrives mid-round in one round, appended as
		// a durable input message that composes to a steering node.
		if round == 1 {
			steer := message.Message{Role: message.RoleInput,
				Content: []message.Content{{Type: message.ContentProse, Text: "actually, stop"}}}
			steer.Steering = true
			appendDurable(steer)
			snap(fmt.Sprintf("round %d: steering", round), nil)
		}
		// The result lands one message later: the invoke node composed in
		// every frame above now MUTATES -- status running -> ok, output from
		// the streamed partial to the clamped durable text.
		t = timings[id]
		t.FinishedAt = 300
		timings[id] = t
		appendDurable(message.Message{Role: message.RoleToolResult, Content: []message.Content{
			{Type: message.ContentToolResult, ToolCallID: id,
				Text: strings.Repeat("durable output line\n", 400)},
		}})
		snap(fmt.Sprintf("round %d: result durable", round), nil)
	}

	// An interrupt repair appends an aborted assistant message with an
	// unmatched tool_invoke, then its synthetic result.
	appendDurable(message.Message{Role: message.RoleOutput, StopReason: message.StopAborted,
		Content: []message.Content{
			{Type: message.ContentProse, Text: "partial work"},
			{Type: message.ContentToolInvoke, ToolCallID: "call_aborted", ToolName: "bash"},
		}})
	snap("repair: aborted assistant", nil)
	appendDurable(message.Message{Role: message.RoleToolResult, Content: []message.Content{
		{Type: message.ContentToolResult, ToolCallID: "call_aborted",
			Text: "interrupted: tool execution was cancelled", IsError: true},
	}})
	snap("repair: synthetic result", nil)

	return frames
}

func TestIncrementalEqualsWholesaleAtEveryFrame(t *testing.T) {
	frames := turnScript(4)
	if len(frames) < 40 {
		t.Fatalf("script produced %d frames, want a turn's worth", len(frames))
	}
	inc := NewIncremental()
	for i, f := range frames {
		want := Nodes(f.msgs, f.partials, f.argPartials, f.timings)
		got := inc.Nodes(f.msgs, f.partials, f.argPartials, f.timings)
		if !reflect.DeepEqual(want, got) {
			t.Fatalf("frame %d (%s): incremental composition diverged from wholesale\n%s",
				i, f.what, diffNodes(want, got))
		}
	}
}

// TestIncrementalActuallyReuses is the other half: an implementation that
// simply calls Nodes every frame would pass the equality test and buy nothing.
// The subject here is the WORK SKIPPED.
func TestIncrementalActuallyReuses(t *testing.T) {
	frames := turnScript(8)
	inc := NewIncremental()
	for _, f := range frames {
		inc.Nodes(f.msgs, f.partials, f.argPartials, f.timings)
	}
	composed, reused := inc.Stats()
	if reused == 0 {
		t.Fatalf("composed %d messages, reused 0: nothing was memoized", composed)
	}
	// Wholesale would compose sum(len(msgs)) over the script. Incremental must
	// be a small fraction of it.
	var wholesale int
	for _, f := range frames {
		wholesale += len(f.msgs)
	}
	if composed > wholesale/4 {
		t.Fatalf("incremental composed %d messages where wholesale composes %d: not enough is being skipped", composed, wholesale)
	}
	t.Logf("composed %d messages, reused %d, wholesale would compose %d", composed, reused, wholesale)
}

// TestIncrementalSurvivesAResetBetweenTurns pins the lifecycle: the memo is
// per turn, and a stale memo from the previous turn must never bleed into the
// next one.
func TestIncrementalSurvivesAResetBetweenTurns(t *testing.T) {
	inc := NewIncremental()
	for _, f := range turnScript(2) {
		inc.Nodes(f.msgs, f.partials, f.argPartials, f.timings)
	}
	inc.Reset()
	for i, f := range turnScript(3) {
		want := Nodes(f.msgs, f.partials, f.argPartials, f.timings)
		if got := inc.Nodes(f.msgs, f.partials, f.argPartials, f.timings); !reflect.DeepEqual(want, got) {
			t.Fatalf("frame %d (%s) after Reset: %s", i, f.what, diffNodes(want, got))
		}
	}
}

// TestIncrementalDropsAMemoItCannotTrust proves the memo degrades to a miss
// rather than to a lie. The region is append-only in the product (repair
// appends, it never rewrites), so this input cannot arise there -- which is
// exactly why the guard needs a test: nothing else would exercise it.
func TestIncrementalDropsAMemoItCannotTrust(t *testing.T) {
	frames := turnScript(3)
	inc := NewIncremental()
	for _, f := range frames {
		inc.Nodes(f.msgs, f.partials, f.argPartials, f.timings)
	}
	// Rewrite history under the memo: same shape, different content.
	last := frames[len(frames)-1]
	rewritten := append([]message.Message(nil), last.msgs...)
	for i := range rewritten {
		if rewritten[i].Role == message.RoleOutput && len(rewritten[i].Content) > 0 {
			blocks := append([]message.Content(nil), rewritten[i].Content...)
			blocks[0].Text = "REWRITTEN"
			rewritten[i].Content = blocks
			break
		}
	}
	want := Nodes(rewritten, last.partials, last.argPartials, last.timings)
	got := inc.Nodes(rewritten, last.partials, last.argPartials, last.timings)
	if !reflect.DeepEqual(want, got) {
		t.Fatalf("a rewritten region served a stale memo:\n%s", diffNodes(want, got))
	}
}

func diffNodes(want, got []livedoc.Node) string {
	var b strings.Builder
	fmt.Fprintf(&b, "  want %d nodes, got %d\n", len(want), len(got))
	n := len(want)
	if len(got) < n {
		n = len(got)
	}
	for i := 0; i < n; i++ {
		if reflect.DeepEqual(want[i], got[i]) {
			continue
		}
		fmt.Fprintf(&b, "  node %d differs:\n    want %+v\n    got  %+v\n", i, want[i], got[i])
		if i > 3 {
			break
		}
	}
	return b.String()
}
