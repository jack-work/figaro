package aria

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/livedoc"
	client "github.com/jack-work/figaro/sdk/aria"
)

// After folding any prefix of a turn's frames, the client can answer what
// was asked and by whom. An absent inquiry on a part means unchanged.
func TestClientAlwaysHasTheQuestion(t *testing.T) {
	const q = "WHATDIDIASK"
	const who = "aria 123456"
	segs := []InquirySegment{{Sender: who, Text: q}}
	drive := func(s *Server) {
		s.OpenInquiry(1, q, nil, segs...)
		s.OpenTurn(1)
		s.Update(nil, []livedoc.Node{{Type: livedoc.NodeProse, Markdown: "answering"}}, 0)
		s.Update(nil, []livedoc.Node{{Type: livedoc.NodeProse, Markdown: "answering more"}}, 0)
		s.Update(nil, []livedoc.Node{{Type: livedoc.NodeProse, Markdown: "answering yet more"}}, 0)
		s.Close()
		s.Seal(nil)
	}

	var frames []Page
	rec := NewServer()
	rec.Subscribe(func(p Page) { frames = append(frames, p) })
	drive(rec)
	if len(frames) < 5 {
		t.Fatalf("fixture broken: only %d frames", len(frames))
	}

	for n := 1; n <= len(frames); n++ {
		c := client.NewClient()
		for _, f := range frames[:n] {
			c.Apply(f, client.Notify)
		}
		got, sender := questionOf(c, 1)
		if got != q {
			t.Fatalf("after %d of %d frames the client cannot answer what was asked: %q",
				n, len(frames), got)
		}
		// The text and its senders travel together: a restatement carrying the
		// text alone would overwrite an attributed question.
		if sender != who {
			t.Fatalf("after %d of %d frames the question lost its sender: %q",
				n, len(frames), sender)
		}
	}
}

// The form state a turn enters with rides the frame that opens it.
func TestTheOpeningFrameCarriesTheFormState(t *testing.T) {
	deltas := map[string]livedoc.FormDelta{
		"a1.system.forked_from": {
			Value: json.RawMessage(`"90ec6584"`), Kind: livedoc.FormBound,
			Event: livedoc.FormSet, Form: "a1",
		},
	}
	s := NewServer()
	var frames []Page
	s.Subscribe(func(p Page) { frames = append(frames, p) })
	s.OpenInquiry(1, "the question", deltas)
	s.OpenTurn(1)
	s.Update(nil, []livedoc.Node{{Type: livedoc.NodeProse, Markdown: "still answering"}}, 0)

	c := client.NewClient()
	fold := func() map[string]livedoc.FormDelta {
		for _, f := range frames {
			c.Apply(f, client.Notify)
		}
		v := c.View()
		for _, m := range append(append([]Message(nil), v.Closed...), open(v)...) {
			if m.Turn == 1 {
				return m.FormDeltas
			}
		}
		return nil
	}
	if _, ok := fold()["a1.system.forked_from"]; !ok {
		t.Fatalf("the fork banner is not on the open turn: %+v", fold())
	}

	s.Close()
	s.Seal(nil)
	if _, ok := fold()["a1.system.forked_from"]; !ok {
		t.Fatalf("the seal dropped the state the turn opened with: %+v", fold())
	}

	forkedFrom := ""
	for _, m := range c.View().Closed {
		if m.Turn == 1 {
			forkedFrom = m.ForkedFrom
		}
	}
	if forkedFrom != "90ec6584" {
		t.Fatalf("the turn does not name its parent after the seal: %q", forkedFrom)
	}
}

// A client that missed the opening frames recovers through the read it
// issues on connect.
func TestLateJoinerRecoversTheQuestionFromARead(t *testing.T) {
	const q = "WHATDIDIASK"
	s := NewServer()
	s.OpenInquiry(1, q, nil)
	s.OpenTurn(1)
	s.Update(nil, []livedoc.Node{{Type: livedoc.NodeProse, Markdown: "answering"}}, 0)

	var late []Page
	s.Subscribe(func(p Page) { late = append(late, p) })
	s.Update(nil, []livedoc.Node{{Type: livedoc.NodeProse, Markdown: "more"}}, 0)

	c := client.NewClient()
	for _, f := range late {
		c.Apply(f, client.Notify)
	}
	if got, _ := questionOf(c, 1); got == q {
		t.Fatal("fixture broken: the late joiner was supposed to miss the question")
	}
	c.Apply(s.Read(Anchor{}, 1<<20), client.Notify)
	if got, _ := questionOf(c, 1); got != q {
		t.Errorf("a read must re-supply the question, got %q", got)
	}
}

func questionOf(c *client.Client, turn int) (question, sender string) {
	v := c.View()
	for _, m := range append(append([]Message(nil), v.Closed...), open(v)...) {
		if m.Turn == turn && m.Inquiry != "" {
			if len(m.InquirySegments) > 0 {
				sender = m.InquirySegments[0].Sender
			}
			return m.Inquiry, sender
		}
	}
	return "", ""
}

func open(v client.View) []Message {
	if v.Open == nil {
		return nil
	}
	return []Message{*v.Open}
}

// Driving the same turn twice with questions of different lengths isolates
// the question's contribution to bytes pushed.
func TestQuestionIsNotRestatedOnEveryFrame(t *testing.T) {
	drive := func(q string) (pushed, frames, carrying int) {
		s := NewServer()
		s.Subscribe(func(p Page) {
			b, _ := json.Marshal(p)
			pushed += len(b)
			frames++
			for _, part := range p.Parts {
				if part.Inquiry != "" {
					carrying++
				}
			}
		})
		s.OpenInquiry(1, q, nil)
		s.OpenTurn(1)
		for i := 0; i < 40; i++ {
			s.Update(nil, []livedoc.Node{{Type: livedoc.NodeProse, Markdown: strings.Repeat("x", i+1)}}, 0)
		}
		s.Close()
		return
	}

	short, frames, carrying := drive("q")
	long := strings.Repeat("a long question that costs real bytes ", 8)
	big, _, _ := drive(long)

	// The delta divided by the question's length is how many times it crossed.
	restatements := float64(big-short) / float64(len(long)-1)
	if restatements > 3.5 {
		t.Errorf("the question crossed the wire ~%.1f times over %d frames; want the establishing few",
			restatements, frames)
	}
	if carrying > 3 {
		t.Errorf("%d of %d frames restate the question", carrying, frames)
	}
	t.Logf("%d frames, %d carry the question, ~%.1f restatements (%d B extra for a %d B question)",
		frames, carrying, restatements, big-short, len(long))
}

func BenchmarkTurnPushBytes(b *testing.B) {
	q := "Use the write tool exactly once to write /var/tmp/x/opera.md: 25 numbered " +
		"lines about Il barbiere di Siviglia. Then run: grep -c . /var/tmp/x/opera.md . " +
		"Then reply with one short sentence."
	b.ReportAllocs()
	var pushed int
	for b.Loop() {
		pushed = 0
		s := NewServer()
		s.Subscribe(func(p Page) {
			enc, _ := json.Marshal(p)
			pushed += len(enc)
		})
		s.OpenInquiry(1, q, nil, InquirySegment{Sender: "aria e83ae209", Text: q})
		s.OpenTurn(1)
		for i := 0; i < 40; i++ {
			s.Update(nil, []livedoc.Node{{
				Type: livedoc.NodeTool, ID: "t1", Name: "write", Status: livedoc.StatusRunning,
				Input: strings.Repeat("a line of the file being written\n", i+1),
			}}, 0)
		}
		s.Close()
		s.Seal(nil)
	}
	b.ReportMetric(float64(pushed), "B/turn")
}

func TestTheSealFrameDoesNotResendTheOpeningDeltas(t *testing.T) {
	delta := func(v string) livedoc.FormDelta {
		return livedoc.FormDelta{Value: json.RawMessage(v), Kind: livedoc.FormBound, Event: livedoc.FormSet, Form: "a1"}
	}
	opening := map[string]livedoc.FormDelta{
		"a1.system.forked_from": delta(`"90ec6584"`),
		"a1.credo.content":      delta(`"the birth credo"`),
	}
	run := func(stamp func(tail *Turn)) (frames []Page, c *client.Client) {
		s := NewServer()
		s.Subscribe(func(p Page) { frames = append(frames, p) })
		s.OpenInquiry(1, "q", opening)
		s.OpenTurn(1)
		s.Update(nil, []livedoc.Node{{Type: livedoc.NodeProse, Markdown: "answer"}}, 0)
		s.Close()
		s.StampTail(func(tail *Turn, _ []Turn) { stamp(tail) })
		s.Seal(nil)
		c = client.NewClient()
		for _, f := range frames {
			c.Apply(f, client.Notify)
		}
		return frames, c
	}
	carried := func(frames []Page) int {
		n := 0
		for _, f := range frames {
			for _, p := range f.Parts {
				n += len(p.FormDeltas)
			}
		}
		return n
	}
	folded := func(c *client.Client) map[string]livedoc.FormDelta {
		for _, m := range c.View().Closed {
			if m.Turn == 1 {
				return m.FormDeltas
			}
		}
		return nil
	}

	frames, c := run(func(tail *Turn) {})
	if got := carried(frames); got != len(opening) {
		t.Fatalf("each opening delta must cross the wire once, %d went out for %d", got, len(opening))
	}
	if len(folded(c)) != len(opening) {
		t.Fatalf("a client folding the frames lost deltas: %+v", folded(c))
	}

	frames, c = run(func(tail *Turn) { tail.FormDeltas["a1.cwd"] = delta(`"/tmp"`) })
	if len(folded(c)) != len(opening)+1 {
		t.Fatalf("a delta stamped at seal never reached the client: %+v", folded(c))
	}
	if carried(frames) <= len(opening) {
		t.Fatalf("the seal dropped a stamped delta: %d carried", carried(frames))
	}
}
