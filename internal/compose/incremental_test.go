package compose

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/livedoc"
	"github.com/jack-work/figaro/api/message"
)

// Incremental composition must equal wholesale composition at every frame,
// not merely at seal.

type frame struct {
	what        string
	msgs        []message.Message
	partials    map[string]string
	argPartials map[string]string
	timings     map[string]ToolTiming
}

// turnScript builds the frames of a turn of r tool rounds, each carrying the
// whole open region.
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

	appendDurable(message.Message{Role: message.RoleInput,
		Content: []message.Content{message.TextContent("do the thing")}})
	snap("prompt", nil)

	for round := 0; round < r; round++ {
		id := fmt.Sprintf("call_%d", round)

		inflight := &message.Message{Role: message.RoleOutput}
		for i := 1; i <= 3; i++ {
			inflight.Content = []message.Content{
				{Type: message.ContentProse, Text: strings.Repeat("thinking out loud. ", i)},
			}
			snap(fmt.Sprintf("round %d: prose frame %d", round, i), inflight)
		}
		inflight.Content = append(inflight.Content,
			message.Content{Type: message.ContentToolInvoke, ToolCallID: id, ToolName: "bash"})
		for i, part := range []string{`{"comm`, `{"command": "ls -`, `{"command": "ls -la"}`} {
			argPartials[id] = part
			timings[id] = ToolTiming{OpenedAt: 100 + int64(i)}
			snap(fmt.Sprintf("round %d: args frame %d", round, i), inflight)
		}
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

		for i := 1; i <= 3; i++ {
			partials[id] = strings.Repeat("streamed output line\n", i*90)
			snap(fmt.Sprintf("round %d: output frame %d", round, i), nil)
		}
		if round == 1 {
			steer := message.Message{Role: message.RoleInput,
				Content: []message.Content{{Type: message.ContentProse, Text: "actually, stop"}}}
			steer.Steering = true
			appendDurable(steer)
			snap(fmt.Sprintf("round %d: steering", round), nil)
		}
		t = timings[id]
		t.FinishedAt = 300
		timings[id] = t
		appendDurable(message.Message{Role: message.RoleToolResult, Content: []message.Content{
			{Type: message.ContentToolResult, ToolCallID: id,
				Text: strings.Repeat("durable output line\n", 400)},
		}})
		snap(fmt.Sprintf("round %d: result durable", round), nil)
	}

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
		gp, gs, _ := inc.Nodes(f.msgs, f.partials, f.argPartials, f.timings)
		got := join(gp, gs)
		if !reflect.DeepEqual(want, got) {
			t.Fatalf("frame %d (%s): incremental composition diverged from wholesale\n%s",
				i, f.what, diffNodes(want, got))
		}
	}
}

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
	var wholesale int
	for _, f := range frames {
		wholesale += len(f.msgs)
	}
	if composed > wholesale/4 {
		t.Fatalf("incremental composed %d messages where wholesale composes %d: not enough is being skipped", composed, wholesale)
	}
	t.Logf("composed %d messages, reused %d, wholesale would compose %d", composed, reused, wholesale)
}

func TestIncrementalSurvivesAResetBetweenTurns(t *testing.T) {
	inc := NewIncremental()
	for _, f := range turnScript(2) {
		inc.Nodes(f.msgs, f.partials, f.argPartials, f.timings)
	}
	inc.Reset()
	for i, f := range turnScript(3) {
		want := Nodes(f.msgs, f.partials, f.argPartials, f.timings)
		gp, gs, _ := inc.Nodes(f.msgs, f.partials, f.argPartials, f.timings)
		if got := join(gp, gs); !reflect.DeepEqual(want, got) {
			t.Fatalf("frame %d (%s) after Reset: %s", i, f.what, diffNodes(want, got))
		}
	}
}

// The region is append-only in production, so nothing else exercises the
// memo's distrust path.
func TestIncrementalDropsAMemoItCannotTrust(t *testing.T) {
	frames := turnScript(3)
	inc := NewIncremental()
	for _, f := range frames {
		inc.Nodes(f.msgs, f.partials, f.argPartials, f.timings)
	}
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
	rp, rs, _ := inc.Nodes(rewritten, last.partials, last.argPartials, last.timings)
	got := join(rp, rs)
	if !reflect.DeepEqual(want, got) {
		t.Fatalf("a rewritten region served a stale memo:\n%s", diffNodes(want, got))
	}
}

func join(prefix, suffix []livedoc.Node) []livedoc.Node {
	if len(prefix)+len(suffix) == 0 {
		return nil
	}
	out := make([]livedoc.Node, 0, len(prefix)+len(suffix))
	out = append(out, prefix...)
	return append(out, suffix...)
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
