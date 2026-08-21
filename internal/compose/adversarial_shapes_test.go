package compose

// ADVERSARIAL SHAPES for the split rule (see prefix_stability_probe_test.go).
//
// turnScript() is one shape: a single tool per round, its result the next
// durable message. The probe reports it never puts a live-map-dependent
// message inside the prefix -- which is why the equality test passes and why
// that pass says NOTHING about memoKey's blindness to partials, argPartials
// and timings.
//
// These scripts are the shapes production can reach that turnScript does not:
//
//	A  parallel invokes in ONE assistant message, results landing one at a
//	   time, with a durable message appended BETWEEN them (a mid-turn steer).
//	B  a duplicate result for one call id (the interrupt/repair race: a
//	   synthetic "cancelled" result for a tool that also reported), while a
//	   sibling tool is still streaming.
//	C  a result whose invoke is OUTSIDE the region (the region starts after
//	   the turn boundary, cf. composeTurn's ReadFrom(turnStartLT+1)).
//	D  a timing stamped AFTER its result message became durable.
//
// Each asks the same question: does stableBoundary let a node whose rendering
// still depends on a live map into the memoized prefix?

import (
	"reflect"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/message"
)

// scriptBuilder accumulates durable messages and live maps, snapshotting the
// frame the agent would hand the projector.
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

// scriptParallelStaggered: A. Two tools dispatched from one assistant message;
// tool a finishes, a steer lands, tool b is still streaming.
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
	// A mid-turn steer: a durable message appended while b still streams.
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

// scriptDuplicateResult: B. An interrupt appends a synthetic cancelled result
// for a call that also reported for real, while a sibling still streams.
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
	// The repair path appends a synthetic result for the same id.
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

// scriptOrphanResult: C. The region begins after a turn boundary, so a result
// arrives for an invoke composed in a PREVIOUS region, while a tool inside
// this region streams.
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

// scriptLateTiming: D. The result message becomes durable BEFORE the timing is
// stamped. Production stamps FinishedAt on toolEnd and appends the result
// afterwards; this script inverts that order to ask what the split rule would
// do if it ever changed.
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

// A, B and C are shapes production can produce (or nearly), and the split
// rule must hold on all of them.
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

// D IS ASSERTED AS A FACT, NOT A WISH.
//
// A timing stamped AFTER its result became durable is invisible to the memo:
// the invoke's result is present, so its message is inside the stable prefix,
// and `timings` is not part of memoKey. The composer would serve FinishedAt:0
// after the stamp landed.
//
// Production cannot reach it, and NOT because of anything in this package:
// internal/figaro stamps finishToolTiming on the toolEnd event and builds the
// tool_result tic afterwards, in the same goroutine (turn.go, the two tool
// loops). That guard is asserted where it lives, in
// internal/figaro/tool_timing_order_test.go.
//
// So this test asserts the blindness rather than demanding it be fixed. If it
// FAILS, the memo has been made timing-aware (or the split rule now excludes
// such a message) and the guard test in internal/figaro is no longer load
// bearing -- both can then be retired together, deliberately.
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
