package aria

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/jack-work/figaro/internal/livedoc"
)

// A CLIENT ALWAYS HAS THE QUESTION, AND KNOWS WHO ASKED IT.
//
// That is the property; "every part carries it" was one way to get it, and an
// expensive one — on a measured tape the restated question was 38% of the
// bytes pushed. A part is a delta against a turn the client already holds, so
// an absent inquiry means UNCHANGED, exactly as an absent node field does.
//
// The bug this replaces is real and must not come back: the question used to
// ride exactly one frame, and a client that subscribed after it held a turn
// with content and no question until the turn ENDED. So the assertion is not
// "the frames contain it" but "after folding any prefix of them, the client
// can answer what was asked".
func TestClientAlwaysHasTheQuestion(t *testing.T) {
	const q = "WHATDIDIASK"
	const who = "aria 123456"
	segs := []InquirySegment{{Sender: who, Text: q}}
	drive := func(s *Server) {
		s.OpenInquiry(1, q, segs...)
		s.OpenTurn(1)
		s.Update([]livedoc.Node{{Type: livedoc.NodeProse, Markdown: "answering"}})
		s.Update([]livedoc.Node{{Type: livedoc.NodeProse, Markdown: "answering more"}})
		s.Update([]livedoc.Node{{Type: livedoc.NodeProse, Markdown: "answering yet more"}})
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

	// Fold every prefix. After each one the client must be able to say what was
	// asked — including the very first frame that mentions the turn.
	for n := 1; n <= len(frames); n++ {
		c := NewClient()
		for _, f := range frames[:n] {
			c.Apply(f)
		}
		got, sender := questionOf(c, 1)
		if got != q {
			t.Fatalf("after %d of %d frames the client cannot answer what was asked: %q",
				n, len(frames), got)
		}
		// The text and its senders travel TOGETHER, always. A restatement
		// carrying the text alone is not a harmless omission: the client holds
		// what a part last said, so it would overwrite an attributed question
		// with an unattributed one and the live surfaces would render a
		// question nobody asked.
		if sender != who {
			t.Fatalf("after %d of %d frames the question lost its sender: %q",
				n, len(frames), sender)
		}
	}
}

// A client that MISSED the opening frames — the case the whole design turns on
// — recovers through the read it is required to issue on connect.
func TestLateJoinerRecoversTheQuestionFromARead(t *testing.T) {
	const q = "WHATDIDIASK"
	s := NewServer()
	s.OpenInquiry(1, q)
	s.OpenTurn(1)
	s.Update([]livedoc.Node{{Type: livedoc.NodeProse, Markdown: "answering"}})

	// Subscribing now: everything so far was missed.
	var late []Page
	s.Subscribe(func(p Page) { late = append(late, p) })
	s.Update([]livedoc.Node{{Type: livedoc.NodeProse, Markdown: "more"}})

	c := NewClient()
	for _, f := range late {
		c.Apply(f)
	}
	if got, _ := questionOf(c, 1); got == q {
		t.Fatal("fixture broken: the late joiner was supposed to miss the question")
	}
	c.Apply(s.Read(Anchor{}, 1<<20))
	if got, _ := questionOf(c, 1); got != q {
		t.Errorf("a read must re-supply the question, got %q", got)
	}
}

// questionOf is what the client can say about a turn, through its own view —
// not by reaching into the frames it was given.
func questionOf(c *Client, turn int) (question, sender string) {
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

func open(v View) []Message {
	if v.Open == nil {
		return nil
	}
	return []Message{*v.Open}
}

// The saving, asserted rather than described — and isolated, by driving the
// SAME turn twice with questions of different lengths. The difference in bytes
// pushed is the question's whole contribution, node traffic having cancelled.
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
		s.OpenInquiry(1, q)
		s.OpenTurn(1)
		for i := 0; i < 40; i++ {
			s.Update([]livedoc.Node{{Type: livedoc.NodeProse, Markdown: strings.Repeat("x", i+1)}})
		}
		s.Close()
		return
	}

	short, frames, carrying := drive("q")
	long := strings.Repeat("a long question that costs real bytes ", 8)
	big, _, _ := drive(long)

	// Every restatement costs the whole question, so the delta divided by the
	// question's length IS the number of times it crossed the wire.
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

// BenchmarkTurnPushBytes measures what a streaming turn costs on the wire: the
// bytes a subscriber is handed for one turn of forty frames, with a question of
// realistic length. It reports B/turn so a before/after is one number.
func BenchmarkTurnPushBytes(b *testing.B) {
	q := "Use the write tool exactly once to write /var/tmp/x/opera.md — 25 numbered " +
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
		s.OpenInquiry(1, q, InquirySegment{Sender: "aria e83ae209", Text: q})
		s.OpenTurn(1)
		for i := 0; i < 40; i++ {
			s.Update([]livedoc.Node{{
				Type: livedoc.NodeTool, ID: "t1", Name: "write", Status: livedoc.StatusRunning,
				Input: strings.Repeat("a line of the file being written\n", i+1),
			}})
		}
		s.Close()
		s.Seal(nil)
	}
	b.ReportMetric(float64(pushed), "B/turn")
}
