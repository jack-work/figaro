package chat

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/aria"
	"github.com/jack-work/figaro/api/livedoc"
	srv "github.com/jack-work/figaro/internal/livelog/aria"
)

type run struct {
	t      *testing.T
	f      *turnFold
	r      *reader
	events []Event
	v      int
}

func newRun(t *testing.T) *run {
	return &run{t: t, f: newTurnFold(1), r: newReader(Cursor{Turn: 1})}
}

func (r *run) part(p aria.TurnPart) applied {
	r.t.Helper()
	got := r.f.apply(p)
	r.r.advance(r.f, r.emit)
	return got
}

func (r *run) emit(e Event) { r.events = append(r.events, e) }

func (r *run) frame(from uint64, nodes ...aria.NodeDelta) {
	r.t.Helper()
	if got := r.part(aria.TurnPart{Turn: aria.Turn{ID: 1, Live: &aria.Live{From: from, V: r.v, Nodes: nodes}}, From: from}); got != fresh {
		r.t.Fatalf("frame v%d: %v", r.v, got)
	}
	r.v++
}

func (r *run) done(reason string) {
	r.f.finish(reason)
	r.r.advance(r.f, r.emit)
}

func set(id uint64, kv map[string]any) aria.NodeDelta {
	kv["role"] = "output"
	return aria.NodeDelta{ID: id, Set: kv}
}

func splice(id uint64, field string, at int, ins string) aria.NodeDelta {
	return aria.NodeDelta{ID: id, Patch: map[string]livedoc.Delta{field: {At: at, Ins: ins}}}
}

func summary(events []Event) string {
	var parts []string
	for _, e := range events {
		switch e.Kind {
		case Delta:
			parts = append(parts, fmt.Sprintf("+%q", e.Text))
		case ToolStart:
			parts = append(parts, "tool.start:"+e.Tool.Name+":"+e.Tool.Args)
		case ToolEnd:
			parts = append(parts, "tool.end:"+e.Tool.Result+":"+e.Tool.Error)
		default:
			parts = append(parts, string(e.Kind)+":"+e.Text)
		}
	}
	return strings.Join(parts, " ")
}

func text(events []Event) string {
	var b strings.Builder
	for _, e := range events {
		if e.Kind == Delta {
			b.WriteString(e.Text)
		}
	}
	return b.String()
}

func TestStreamedProseBecomesTextDeltasThatSumToTheReply(t *testing.T) {
	r := newRun(t)
	r.frame(0, set(0, map[string]any{"type": "prose", "markdown": "Hel"}))
	r.frame(0, splice(0, "markdown", 3, "lo"))
	r.frame(0, splice(0, "markdown", 5, " world"))
	r.done("")
	if got := summary(r.events); got != `+"Hel" +"lo" +" world" end:Hello world` {
		t.Fatalf("events: %s", got)
	}
}

func TestToolsEndOnTheirStatus(t *testing.T) {
	r := newRun(t)
	r.frame(0, set(0, map[string]any{"type": "tool", "name": "read", "status": "running", "input": `{"path":"a"}`}))
	r.frame(0, aria.NodeDelta{ID: 0, Set: map[string]any{"status": "ok", "output": "contents"}})
	r.frame(0, set(1, map[string]any{"type": "tool", "name": "bash", "status": "running"}))
	r.frame(0, aria.NodeDelta{ID: 1, Set: map[string]any{"status": "error", "summary": "exit 2", "output": "boom"}})
	r.done("")
	want := `tool.start:read:{"path":"a"} tool.end:contents: tool.start:bash: tool.end:boom:exit 2 end:`
	if got := summary(r.events); got != want {
		t.Fatalf("events:\n got %s\nwant %s", got, want)
	}
}

func TestAToolStartsOnlyOnceItsArgumentsAreWhole(t *testing.T) {
	r := newRun(t)
	r.frame(0, set(0, map[string]any{"type": "tool", "name": "bash", "status": "running", "input": `{"comm`}))
	if len(r.events) != 0 {
		t.Fatalf("started on a partial input: %s", summary(r.events))
	}
	r.frame(0, aria.NodeDelta{ID: 0, Set: map[string]any{"args": map[string]any{"command": "ls"}}, Unset: []string{"input"}})
	if got := summary(r.events); got != `tool.start:bash:{"command":"ls"}` {
		t.Fatalf("events: %s", got)
	}
}

func TestAToolThatFinishesFirstWaitsForTheToolBeforeIt(t *testing.T) {
	r := newRun(t)
	r.frame(0, set(0, map[string]any{"type": "tool", "name": "slow", "status": "running"}), set(1, map[string]any{"type": "tool", "name": "fast", "status": "running"}))
	r.frame(0, aria.NodeDelta{ID: 1, Set: map[string]any{"status": "ok", "output": "f"}})
	if got := summary(r.events); got != "tool.start:slow: tool.start:fast:" {
		t.Fatalf("fast ended before slow: %s", got)
	}
	r.frame(0, aria.NodeDelta{ID: 0, Set: map[string]any{"status": "ok", "output": "s"}})
	if got := summary(r.events); got != "tool.start:slow: tool.start:fast: tool.end:s: tool.end:f:" {
		t.Fatalf("events: %s", got)
	}
}

func TestThinkingIsNeverShown(t *testing.T) {
	r := newRun(t)
	r.frame(0, set(0, map[string]any{"type": "thinking", "markdown": "secret plan"}), set(1, map[string]any{"type": "prose", "markdown": "Answer"}))
	r.done("")
	if got := summary(r.events); got != `+"Answer" end:Answer` {
		t.Fatalf("events: %s", got)
	}
}

func TestProseAfterAToolIsSeparatedByAParagraphBreak(t *testing.T) {
	r := newRun(t)
	r.frame(0, set(0, map[string]any{"type": "prose", "markdown": "Looking."}), set(1, map[string]any{"type": "tool", "name": "read", "status": "ok"}), set(2, map[string]any{"type": "prose", "markdown": "Found it."}))
	r.done("")
	if got := summary(r.events); got != `+"Looking." tool.start:read: tool.end:: +"\n\nFound it." end:Looking.

Found it.` {
		t.Fatalf("events: %s", got)
	}
}

func TestAnErrorTurnFailsWithItsReason(t *testing.T) {
	r := newRun(t)
	r.frame(0, set(0, map[string]any{"type": "prose", "markdown": "Par"}))
	r.done("error: provider overloaded")
	if got := summary(r.events); got != `+"Par" error:provider overloaded` {
		t.Fatalf("events: %s", got)
	}
}

func TestAHugeToolResultIsClipped(t *testing.T) {
	r := newRun(t)
	r.frame(0, set(0, map[string]any{"type": "tool", "name": "cat", "status": "ok", "output": strings.Repeat("é", 5000)}))
	var end Event
	for _, e := range r.events {
		if e.Kind == ToolEnd {
			end = e
		}
	}
	if end.Tool == nil || len(end.Tool.Result) > MaxToolText+4 || !strings.HasSuffix(end.Tool.Result, "…") {
		t.Fatalf("tool end %+v", end)
	}
}

func TestSteeringTextFromTheUserIsNotEchoed(t *testing.T) {
	r := newRun(t)
	r.frame(0, set(0, map[string]any{"type": "prose", "markdown": "Working"}), aria.NodeDelta{ID: 1, Set: map[string]any{"type": "steering", "role": "input", "markdown": "also do x"}})
	r.done("")
	if got := summary(r.events); got != `+"Working" end:Working` {
		t.Fatalf("events: %s", got)
	}
}

func TestProseInALaterSuffixOfTheSameTurnIsSeparatedFromTheEarlierOne(t *testing.T) {
	r := newRun(t)
	r.frame(0, set(0, map[string]any{"type": "prose", "markdown": "First."}))
	if got := r.part(aria.TurnPart{Turn: aria.Turn{ID: 1, Live: &aria.Live{From: 0, V: 0}}}); got != fresh {
		t.Fatalf("close marker: %v", got)
	}
	r.v = 0
	r.frame(1, set(1, map[string]any{"type": "prose", "markdown": "Second."}))
	r.done("")
	if got := text(r.events); got != "First.\n\nSecond." {
		t.Fatalf("text %q from %s", got, summary(r.events))
	}
}

func TestAFrameAlreadyCoveredByASnapshotIsSkipped(t *testing.T) {
	r := newRun(t)
	snap := aria.TurnPart{Turn: aria.Turn{ID: 1, Nodes: []livedoc.Node{{Type: livedoc.NodeProse, Role: livedoc.RoleOutput, Markdown: "Hello"}}, Live: &aria.Live{From: 0, V: 1}}}
	if got := r.part(snap); got != fresh {
		t.Fatalf("snapshot: %v", got)
	}
	if got := r.part(aria.TurnPart{Turn: aria.Turn{ID: 1, Live: &aria.Live{From: 0, V: 1, Nodes: []aria.NodeDelta{splice(0, "markdown", 3, "lo")}}}}); got != stale {
		t.Fatalf("v1 again: %v", got)
	}
	if got := r.part(aria.TurnPart{Turn: aria.Turn{ID: 1, Live: &aria.Live{From: 0, V: 2, Nodes: []aria.NodeDelta{splice(0, "markdown", 5, "!")}}}}); got != fresh {
		t.Fatalf("v2: %v", got)
	}
	if got := text(r.events); got != "Hello!" {
		t.Fatalf("text %q", got)
	}
}

func TestAMissedFrameIsAGap(t *testing.T) {
	r := newRun(t)
	r.frame(0, set(0, map[string]any{"type": "prose", "markdown": "a"}))
	if got := r.f.apply(aria.TurnPart{Turn: aria.Turn{ID: 1, Live: &aria.Live{From: 0, V: 2, Nodes: []aria.NodeDelta{splice(0, "markdown", 2, "c")}}}}); got != gap {
		t.Fatalf("v2 after v0: %v", got)
	}
}

func TestJoiningMidSuffixIsAGap(t *testing.T) {
	f := newTurnFold(1)
	if got := f.apply(aria.TurnPart{Turn: aria.Turn{ID: 1, Live: &aria.Live{From: 0, V: 4, Nodes: []aria.NodeDelta{splice(0, "markdown", 9, "x")}}}}); got != gap {
		t.Fatalf("joined at v4: %v", got)
	}
	if got := f.apply(aria.TurnPart{Turn: aria.Turn{ID: 1, Live: &aria.Live{From: 3, V: 0, Nodes: []aria.NodeDelta{set(3, map[string]any{"type": "prose", "markdown": "x"})}}}, From: 3}); got != gap {
		t.Fatalf("joined at a later suffix with no head: %v", got)
	}
}

func TestAReaderResumedFromACursorGetsOnlyWhatFollows(t *testing.T) {
	r := newRun(t)
	r.frame(0, set(0, map[string]any{"type": "prose", "markdown": "Hello"}), set(1, map[string]any{"type": "tool", "name": "read", "status": "running"}))
	cut := r.events[len(r.events)-1].Cursor
	r.frame(0, aria.NodeDelta{ID: 1, Set: map[string]any{"status": "ok", "output": "x"}}, set(2, map[string]any{"type": "prose", "markdown": "Done."}))
	r.done("")
	whole := summary(r.events)

	again := newReader(cut)
	var events []Event
	again.advance(r.f, func(e Event) { events = append(events, e) })
	if got := summary(events); got != `tool.start:read: tool.end:x: +"\n\nDone." end:Hello

Done.` {
		t.Fatalf("resumed at %+v: %s\nwhole: %s", cut, got, whole)
	}
}

func TestEveryEventCursorCountsWhatWasDelivered(t *testing.T) {
	r := newRun(t)
	r.frame(0, set(0, map[string]any{"type": "prose", "markdown": "Hi"}), set(1, map[string]any{"type": "tool", "name": "read", "status": "ok"}))
	want := []Cursor{{1, 2, 0, 0}, {1, 2, 1, 0}, {1, 2, 1, 1}}
	for i, e := range r.events {
		if e.Cursor != want[i] {
			t.Fatalf("event %d %s at %+v, want %+v", i, e.Kind, e.Cursor, want[i])
		}
	}
}

func TestASecondRoundInTheSameTurnStreamsDeltaByDelta(t *testing.T) {
	s := srv.NewServer()
	var pages []aria.Page
	s.Subscribe(func(p aria.Page) {
		b, _ := json.Marshal(p)
		var q aria.Page
		_ = json.Unmarshal(b, &q)
		pages = append(pages, q)
	})
	prose := func(md string) livedoc.Node {
		return livedoc.Node{Type: livedoc.NodeProse, Role: livedoc.RoleOutput, Markdown: md}
	}
	tool := func(status string) livedoc.Node {
		return livedoc.Node{Type: livedoc.NodeTool, Role: livedoc.RoleOutput, Name: "bash", Status: status, Args: map[string]any{"command": "ls"}}
	}
	s.OpenTurn(1)
	s.Update(nil, []livedoc.Node{prose("Let me")}, 0)
	s.Update(nil, []livedoc.Node{prose("Let me check"), tool("running")}, 0)
	s.Update(nil, []livedoc.Node{prose("Let me check"), tool("ok")}, 0)
	s.Close()
	s.OpenTurn(1)
	s.Update(nil, []livedoc.Node{prose("Let me check"), tool("ok"), prose("Now")}, 0)
	s.Update(nil, []livedoc.Node{prose("Let me check"), tool("ok"), prose("Now the answer")}, 0)
	s.Close()

	f, r := newTurnFold(1), newReader(Cursor{Turn: 1})
	var events []Event
	for _, p := range pages {
		for _, part := range p.Parts {
			if got := f.apply(part); got != fresh {
				t.Fatalf("frame %+v: %v", part.Live, got)
			}
			r.advance(f, func(e Event) { events = append(events, e) })
		}
	}
	want := `+"Let me" +" check" tool.start:bash:{"command":"ls"} tool.end:: +"\n\nNow" +" the answer"`
	if got := summary(events); got != want {
		t.Fatalf("events:\n got %s\nwant %s", got, want)
	}
}

func TestASnapshotBetweenRoundsCoversTheClosedRoundButNotTheNext(t *testing.T) {
	f := newTurnFold(1)
	snap := aria.TurnPart{Turn: aria.Turn{ID: 1, Nodes: []livedoc.Node{{Type: livedoc.NodeProse, Role: livedoc.RoleOutput, Markdown: "Hello"}}}}
	if got := f.apply(snap); got != fresh {
		t.Fatalf("snapshot: %v", got)
	}
	if got := f.apply(aria.TurnPart{Turn: aria.Turn{ID: 1, Live: &aria.Live{From: 0, V: 3, Nodes: []aria.NodeDelta{splice(0, "markdown", 0, "x")}}}}); got != stale {
		t.Fatalf("a frame of the closed round: %v", got)
	}
	if got := f.apply(aria.TurnPart{Turn: aria.Turn{ID: 1, Live: &aria.Live{From: 1, V: 0, Nodes: []aria.NodeDelta{set(1, map[string]any{"type": "prose", "markdown": "more"})}}}, From: 1}); got != fresh {
		t.Fatalf("the next round: %v", got)
	}
	if got := f.reply(); got != "Hello\n\nmore" {
		t.Fatalf("reply %q", got)
	}
}

func TestACloseMarkerForAnUnseenRoundIsAGap(t *testing.T) {
	r := newRun(t)
	r.frame(0, set(0, map[string]any{"type": "prose", "markdown": "a"}))
	if got := r.f.apply(aria.TurnPart{Turn: aria.Turn{ID: 1, Live: &aria.Live{From: 0, V: 4}}}); got != gap {
		t.Fatalf("close at v4 after v0: %v", got)
	}
	if got := newTurnFold(1).apply(aria.TurnPart{Turn: aria.Turn{ID: 1, Live: &aria.Live{From: 0, V: 0}}}); got != gap {
		t.Fatalf("close on a fold that saw nothing: %v", got)
	}
}

func TestAnAdvanceWithNothingNewBuildsNothing(t *testing.T) {
	f := newTurnFold(1)
	var nodes []livedoc.Node
	for i := range 200 {
		nodes = append(nodes,
			livedoc.Node{Type: livedoc.NodeProse, Role: livedoc.RoleOutput, Markdown: fmt.Sprintf("step %d", i)},
			livedoc.Node{Type: livedoc.NodeTool, Role: livedoc.RoleOutput, Name: "bash", Status: livedoc.StatusOK, Args: map[string]any{"n": i}})
	}
	f.apply(aria.TurnPart{Turn: aria.Turn{ID: 1, Nodes: nodes}})
	r := newReader(Cursor{Turn: 1})
	r.advance(f, func(Event) {})
	if allocs := testing.AllocsPerRun(20, func() { r.advance(f, func(Event) {}) }); allocs > 0 {
		t.Fatalf("an advance over 400 unchanged nodes allocated %.0f times", allocs)
	}
}

func TestToolsWholeInTheSameFrameStartTogether(t *testing.T) {
	r := newRun(t)
	r.frame(0, set(0, map[string]any{"type": "tool", "name": "a", "status": "running", "args": map[string]any{"x": 1}}), set(1, map[string]any{"type": "tool", "name": "b", "status": "running", "args": map[string]any{"y": 2}}))
	if got := summary(r.events); got != `tool.start:a:{"x":1} tool.start:b:{"y":2}` {
		t.Fatalf("events: %s", got)
	}
}
