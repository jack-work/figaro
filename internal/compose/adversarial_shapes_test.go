package compose

// Shapes turnScript does not reach:
//
//	A  parallel invokes in one assistant message, results landing one at a
//	   time, with a steer appended between them.
//	B  a duplicate result for one call id while a sibling still streams.
//	C  a result whose invoke is outside the region.
//	D  a timing stamped after its result message became durable.

import (
	"reflect"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/message"
)

type scriptBuilder struct {
	frames      []frame
	durable     []message.Message
	partials    map[string]string
	argPartials map[string]string
	timings     map[string]ToolTiming
	lt          uint64
}

func newScript() *scriptBuilder {
	return &scriptBuilder{
		partials:    map[string]string{},
		argPartials: map[string]string{},
		timings:     map[string]ToolTiming{},
	}
}

func (s *scriptBuilder) appendDurable(m message.Message) {
	s.lt++
	m.LogicalTime = s.lt
	s.durable = append(s.durable, m)
}

func (s *scriptBuilder) snap(what string) {
	f := frame{what: what,
		msgs:        append([]message.Message(nil), s.durable...),
		partials:    map[string]string{},
		argPartials: map[string]string{},
		timings:     map[string]ToolTiming{},
	}
	for k, v := range s.partials {
		f.partials[k] = v
	}
	for k, v := range s.argPartials {
		f.argPartials[k] = v
	}
	for k, v := range s.timings {
		f.timings[k] = v
	}
	s.frames = append(s.frames, f)
}

func advInvoke(id string) message.Content {
	return message.Content{Type: message.ContentToolInvoke, ToolCallID: id, ToolName: "bash",
		Arguments: map[string]any{"command": "seq 1 90"}}
}

func advResult(id, text string, isErr bool) message.Message {
	return message.Message{Role: message.RoleToolResult, Content: []message.Content{
		{Type: message.ContentToolResult, ToolCallID: id, Text: text, IsError: isErr}}}
}

func advProse(text string) message.Content {
	return message.Content{Type: message.ContentProse, Text: text}
}

func advSteer(text string) message.Message {
	m := message.Message{Role: message.RoleInput, Content: []message.Content{advProse(text)}}
	m.Steering = true
	return m
}

func scriptParallelStaggered() []frame {
	s := newScript()
	s.appendDurable(message.Message{Role: message.RoleInput, Content: []message.Content{advProse("do two things")}})
	s.snap("prompt")
	s.appendDurable(message.Message{Role: message.RoleOutput, Content: []message.Content{
		advProse("running both"), advInvoke("call_a"), advInvoke("call_b")}})
	s.timings["call_a"] = ToolTiming{OpenedAt: 100, StartedAt: 200}
	s.timings["call_b"] = ToolTiming{OpenedAt: 101, StartedAt: 201}
	s.snap("assistant durable, both tools running")
	for i := 1; i <= 3; i++ {
		s.partials["call_a"] = strings.Repeat("a-out\n", i*10)
		s.partials["call_b"] = strings.Repeat("b-out\n", i*10)
		s.snap("both streaming")
	}
	t := s.timings["call_a"]
	t.FinishedAt = 300
	s.timings["call_a"] = t
	s.appendDurable(advResult("call_a", strings.Repeat("a-durable\n", 20), false))
	s.snap("result a durable, b still running")
	for i := 4; i <= 6; i++ {
		s.partials["call_b"] = strings.Repeat("b-out\n", i*10)
		s.snap("b streaming after a's result")
	}
	s.appendDurable(advSteer("actually hurry up"))
	s.snap("steer while b runs")
	for i := 7; i <= 9; i++ {
		s.partials["call_b"] = strings.Repeat("b-out\n", i*10)
		s.snap("b streaming after the steer")
	}
	t = s.timings["call_b"]
	t.FinishedAt = 400
	s.timings["call_b"] = t
	s.appendDurable(advResult("call_b", strings.Repeat("b-durable\n", 20), false))
	s.snap("result b durable")
	s.appendDurable(message.Message{Role: message.RoleOutput, Content: []message.Content{advProse("done")}})
	s.snap("closing prose")
	return s.frames
}

func scriptDuplicateResult() []frame {
	s := newScript()
	s.appendDurable(message.Message{Role: message.RoleInput, Content: []message.Content{advProse("go")}})
	s.snap("prompt")
	s.appendDurable(message.Message{Role: message.RoleOutput, Content: []message.Content{
		advProse("two tools"), advInvoke("call_a"), advInvoke("call_b")}})
	s.timings["call_a"] = ToolTiming{OpenedAt: 100, StartedAt: 200}
	s.timings["call_b"] = ToolTiming{OpenedAt: 101, StartedAt: 201}
	s.snap("assistant durable")
	for i := 1; i <= 2; i++ {
		s.partials["call_a"] = strings.Repeat("a-out\n", i*10)
		s.partials["call_b"] = strings.Repeat("b-out\n", i*10)
		s.snap("both streaming")
	}
	t := s.timings["call_a"]
	t.FinishedAt = 300
	s.timings["call_a"] = t
	s.appendDurable(advResult("call_a", "a done", false))
	s.snap("a's real result")
	s.appendDurable(advResult("call_a", "interrupted: tool execution was cancelled", true))
	s.snap("a's synthetic result (duplicate id)")
	for i := 3; i <= 6; i++ {
		s.partials["call_b"] = strings.Repeat("b-out\n", i*10)
		s.snap("b streaming under a doubled result count")
	}
	t = s.timings["call_b"]
	t.FinishedAt = 400
	s.timings["call_b"] = t
	s.appendDurable(advResult("call_b", "b done", false))
	s.snap("b result durable")
	return s.frames
}

func scriptOrphanResult() []frame {
	s := newScript()
	s.appendDurable(advResult("call_before_the_region", "landed late", false))
	s.snap("orphan result opens the region")
	s.appendDurable(message.Message{Role: message.RoleOutput, Content: []message.Content{
		advProse("now mine"), advInvoke("call_x")}})
	s.timings["call_x"] = ToolTiming{OpenedAt: 100, StartedAt: 200}
	s.snap("assistant durable, x running")
	for i := 1; i <= 4; i++ {
		s.partials["call_x"] = strings.Repeat("x-out\n", i*10)
		s.snap("x streaming")
	}
	s.appendDurable(advSteer("keep going"))
	s.snap("steer while x runs")
	for i := 5; i <= 7; i++ {
		s.partials["call_x"] = strings.Repeat("x-out\n", i*10)
		s.snap("x streaming after the steer")
	}
	t := s.timings["call_x"]
	t.FinishedAt = 300
	s.timings["call_x"] = t
	s.appendDurable(advResult("call_x", "x durable", false))
	s.snap("x result durable")
	return s.frames
}

func scriptLateTiming() []frame {
	s := newScript()
	s.appendDurable(message.Message{Role: message.RoleInput, Content: []message.Content{advProse("go")}})
	s.snap("prompt")
	s.appendDurable(message.Message{Role: message.RoleOutput, Content: []message.Content{
		advProse("one tool"), advInvoke("call_a")}})
	s.timings["call_a"] = ToolTiming{OpenedAt: 100, StartedAt: 200}
	s.snap("assistant durable")
	s.partials["call_a"] = strings.Repeat("out\n", 10)
	s.snap("streaming")
	s.appendDurable(advResult("call_a", "done", false))
	s.snap("result durable, timing NOT yet stamped")
	s.appendDurable(message.Message{Role: message.RoleOutput, Content: []message.Content{advProse("after")}})
	s.snap("another message, prefix now covers the invoke")
	t := s.timings["call_a"]
	t.FinishedAt = 300
	s.timings["call_a"] = t
	s.snap("FinishedAt stamped after the fact")
	return s.frames
}

func TestSplitRuleHoldsOnAdversarialShapes(t *testing.T) {
	for _, tc := range []struct {
		name   string
		frames []frame
	}{
		{"A parallel/staggered", scriptParallelStaggered()},
		{"B duplicate result", scriptDuplicateResult()},
		{"C orphan result", scriptOrphanResult()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res := probeScript(t, tc.name, tc.frames, stableBoundary)
			for _, v := range res.violations {
				t.Error(v)
			}
			inc := NewIncremental()
			for i, f := range tc.frames {
				want := Nodes(f.msgs, f.partials, f.argPartials, f.timings)
				gp, gs, _ := inc.Nodes(f.msgs, f.partials, f.argPartials, f.timings)
				if got := join(gp, gs); !reflect.DeepEqual(want, got) {
					t.Fatalf("frame %d (%s): incremental diverged from wholesale\n%s", i, f.what, diffNodes(want, got))
				}
			}
		})
	}
}

// timings is not part of memoKey, so a timing stamped after its result became
// durable goes stale in the memo. internal/figaro stamps before appending the
// result (tool_timing_order_test.go there); this pins the blindness that guard
// exists for.
func TestPrefixIsBlindToATimingStampedAfterDurability(t *testing.T) {
	frames := scriptLateTiming()
	inc := NewIncremental()
	diverged := false
	for _, f := range frames {
		want := Nodes(f.msgs, f.partials, f.argPartials, f.timings)
		gp, gs, _ := inc.Nodes(f.msgs, f.partials, f.argPartials, f.timings)
		if got := join(gp, gs); !reflect.DeepEqual(want, got) {
			diverged = true
		}
	}
	if !diverged {
		t.Fatalf("the memo no longer goes stale on a timing stamped after durability.\n" +
			"That is an improvement, not a failure: memoKey has become timing-aware, or the\n" +
			"boundary now excludes such a message. Retire this test AND the ordering guard it\n" +
			"documents (internal/figaro/tool_timing_order_test.go), deliberately and together.")
	}
}
