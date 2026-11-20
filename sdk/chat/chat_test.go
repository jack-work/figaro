package chat_test

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/sdk"
	"github.com/jack-work/figaro/sdk/chat"
)

func collect(t *testing.T, ch <-chan chat.Event, within time.Duration) []chat.Event {
	t.Helper()
	var out []chat.Event
	timeout := time.After(within)
	for {
		select {
		case e, ok := <-ch:
			if !ok {
				return out
			}
			out = append(out, e)
		case <-timeout:
			t.Fatalf("no end within %s; got %s", within, kinds(out))
		}
	}
}

func kinds(events []chat.Event) string {
	var b []string
	for _, e := range events {
		b = append(b, string(e.Kind))
	}
	return strings.Join(b, " ")
}

func replyText(events []chat.Event) string {
	var b strings.Builder
	for _, e := range events {
		if e.Kind == chat.Delta {
			b.WriteString(e.Text)
		}
	}
	return b.String()
}

func thread(t *testing.T) (*chat.Client, string, *daemon) {
	t.Helper()
	d := newDaemon(t)
	root := d.root(t)
	c := chat.New(d.endpoint())
	t.Cleanup(func() { c.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	id, err := c.Open(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	return c, id, d
}

func TestAReplyStreamsAsSeveralDeltasAndEndsWithItsWholeText(t *testing.T) {
	c, id, _ := thread(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	ch, err := c.Prompt(ctx, id, "words 8 20")
	if err != nil || ch == nil {
		t.Fatalf("prompt: %v %v", ch, err)
	}
	events := collect(t, ch, 30*time.Second)
	last := events[len(events)-1]
	want := "w0 w1 w2 w3 w4 w5 w6 w7"
	if last.Kind != chat.End || replyText(events) != want || last.Text != want {
		t.Fatalf("events %s, text %q, end %q", kinds(events), replyText(events), last.Text)
	}
	if n := strings.Count(kinds(events), "delta"); n < 3 {
		t.Fatalf("the reply came in %d deltas, not streamed: %s", n, kinds(events))
	}
}

func TestAToolCallStreamsBetweenTheProseAroundIt(t *testing.T) {
	c, id, _ := thread(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	ch, err := c.Prompt(ctx, id, "tool")
	if err != nil || ch == nil {
		t.Fatalf("prompt: %v %v", ch, err)
	}
	events := collect(t, ch, 30*time.Second)
	var start, end *chat.Tool
	for _, e := range events {
		switch e.Kind {
		case chat.ToolStart:
			start = e.Tool
		case chat.ToolEnd:
			end = e.Tool
		}
	}
	if start == nil || start.Name != "bash" || !strings.Contains(start.Args, "echo hi") {
		t.Fatalf("tool start %+v in %s", start, kinds(events))
	}
	if end == nil || end.ID != start.ID || !strings.Contains(end.Result, "hi") || end.Error != "" {
		t.Fatalf("tool end %+v", end)
	}
	if got := replyText(events); got != "The tool said hi." {
		t.Fatalf("text %q in %s", got, kinds(events))
	}
	if events[len(events)-1].Kind != chat.End {
		t.Fatalf("events %s", kinds(events))
	}
}

func TestAPromptDuringARunningTurnSteersAndReturnsNoChannel(t *testing.T) {
	c, id, _ := thread(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	ch, err := c.Prompt(ctx, id, "words 20 50")
	if err != nil {
		t.Fatal(err)
	}
	<-ch
	steer, err := c.Prompt(ctx, id, "also this")
	if err != nil || steer != nil {
		t.Fatalf("steer: %v %v", steer, err)
	}
	events := collect(t, ch, 30*time.Second)
	if events[len(events)-1].Kind != chat.End {
		t.Fatalf("events %s", kinds(events))
	}
}

func resumeSplits(t *testing.T, fresh bool) {
	c, id, d := thread(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	sub, stop := context.WithCancel(ctx)
	ch, err := c.Prompt(sub, id, "words 30 40")
	if err != nil {
		t.Fatal(err)
	}
	var before []chat.Event
	for e := range ch {
		before = append(before, e)
		if len(before) == 5 {
			break
		}
	}
	stop()
	for range ch {
	}
	cut := before[len(before)-1].Cursor
	again := c
	if fresh {
		again = chat.New(d.endpoint())
		defer again.Close()
	}
	rest, err := again.Resume(ctx, id, cut)
	if err != nil {
		t.Fatal(err)
	}
	after := collect(t, rest, 30*time.Second)
	whole := replyText(before) + replyText(after)
	var want []string
	for i := range 30 {
		want = append(want, "w"+strconv.Itoa(i))
	}
	if whole != strings.Join(want, " ") {
		t.Fatalf("cut at %+v\nbefore %q\nafter  %q", cut, replyText(before), replyText(after))
	}
	if last := after[len(after)-1]; last.Kind != chat.End || last.Text != whole {
		t.Fatalf("end %+v", last)
	}
}

func TestResumingMidTurnInTheSameClientLosesAndRepeatsNothing(t *testing.T) {
	resumeSplits(t, false)
}

func TestResumingMidTurnFromAnotherClientLosesAndRepeatsNothing(t *testing.T) {
	resumeSplits(t, true)
}

func TestResumingAFinishedTurnReplaysItsRestAndEnds(t *testing.T) {
	c, id, d := thread(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	ch, err := c.Prompt(ctx, id, "words 6 0")
	if err != nil {
		t.Fatal(err)
	}
	events := collect(t, ch, 30*time.Second)
	cut := events[0].Cursor
	other := chat.New(d.endpoint())
	defer other.Close()
	rest, err := other.Resume(ctx, id, cut)
	if err != nil {
		t.Fatal(err)
	}
	after := collect(t, rest, 30*time.Second)
	if events[0].Text+replyText(after) != "w0 w1 w2 w3 w4 w5" || after[len(after)-1].Kind != chat.End {
		t.Fatalf("first %q then %q (%s)", events[0].Text, replyText(after), kinds(after))
	}
}

func TestHistoryReadsPromptsRepliesAndToolsOldestFirst(t *testing.T) {
	c, id, _ := thread(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	for _, p := range []string{"words 2 0", "tool", "words 3 0"} {
		ch, err := c.Prompt(ctx, id, p)
		if err != nil {
			t.Fatal(err)
		}
		collect(t, ch, 30*time.Second)
	}
	h, err := c.History(ctx, id, 0, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(h.Turns) != 3 || !h.More {
		t.Fatalf("history %+v", h)
	}
	first, mid, last := h.Turns[0], h.Turns[1], h.Turns[2]
	if !strings.HasSuffix(first.Prompt, "words 2 0") || first.Reply != "w0 w1" || !first.Done {
		t.Fatalf("first %+v", first)
	}
	if mid.Cursor != (chat.Cursor{Turn: mid.ID, Text: len(mid.Reply), Started: 1, Ended: 1}) || len(mid.Tools) != 1 || mid.Tools[0].Name != "bash" || !strings.Contains(mid.Tools[0].Result, "hi") || mid.Reply != "The tool said hi." {
		t.Fatalf("tool turn %+v", mid)
	}
	if last.Reply != "w0 w1 w2" || !(first.ID < mid.ID && mid.ID < last.ID) || last.Cursor != (chat.Cursor{Turn: last.ID, Text: len(last.Reply)}) {
		t.Fatalf("last %+v", last)
	}
	older, err := c.History(ctx, id, first.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(older.Turns) == 0 || older.Turns[len(older.Turns)-1].ID >= first.ID || older.More {
		t.Fatalf("older %+v", older)
	}
}

// The package must add next to nothing between a frame's arrival and its
// delta: voice mode speaks the first delta.
func TestTheFirstDeltaTrailsTheRawFrameByLittle(t *testing.T) {
	c, id, d := thread(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	var once sync.Once
	rawAt := make(chan time.Time, 1)
	raw, err := sdk.DialAria(sdk.Addr{Door: d.endpoint(), ID: id}, func(method string, params json.RawMessage) {
		if method == rpc.MethodAriaFrame && strings.Contains(string(params), `"w0`) {
			once.Do(func() { rawAt <- time.Now() })
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()

	sent := time.Now()
	ch, err := c.Prompt(ctx, id, "words 4 200")
	if err != nil {
		t.Fatal(err)
	}
	var first time.Time
	for e := range ch {
		if e.Kind == chat.Delta && first.IsZero() {
			first = time.Now()
		}
	}
	frame := <-rawAt
	lag := first.Sub(frame)
	t.Logf("prompt to raw frame %s, prompt to first delta %s, lag %s", frame.Sub(sent), first.Sub(sent), lag)
	if lag > 50*time.Millisecond {
		t.Fatalf("the first delta trailed the raw frame by %s", lag)
	}
}

func TestARefusedTurnFailsWithTheProviderReason(t *testing.T) {
	c, id, _ := thread(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	ch, err := c.Prompt(ctx, id, "fail")
	if err != nil {
		t.Fatal(err)
	}
	events := collect(t, ch, 30*time.Second)
	last := events[len(events)-1]
	if last.Kind != chat.Failed || !strings.Contains(last.Text, "stub refused") {
		t.Fatalf("events %s, last %+v", kinds(events), last)
	}
}

func TestCancelEndsTheRunningTurn(t *testing.T) {
	c, id, _ := thread(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	ch, err := c.Prompt(ctx, id, "words 200 50")
	if err != nil {
		t.Fatal(err)
	}
	<-ch
	if err := c.Cancel(ctx, id); err != nil {
		t.Fatal(err)
	}
	events := collect(t, ch, 15*time.Second)
	if n := strings.Count(replyText(events), " "); n > 150 {
		t.Fatalf("the turn ran on after cancel: %d words", n)
	}
}

func TestADaemonRestartUnderAListenerCarriesOnWithoutRepeating(t *testing.T) {
	c, id, d := thread(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	ch, err := c.Prompt(ctx, id, "words 100 50")
	if err != nil {
		t.Fatal(err)
	}
	var events []chat.Event
	for e := range ch {
		events = append(events, e)
		if len(events) == 3 {
			break
		}
	}
	seen := replyText(events)
	d.must(t, "stop")
	d.must(t, "ls")
	events = append(events, collect(t, ch, time.Minute)...)
	last := events[len(events)-1]
	if last.Kind != chat.End {
		t.Fatalf("events %s, last %+v", kinds(events), last)
	}
	got := replyText(events)
	if !strings.HasPrefix(got, seen) || strings.Count(got, "w0 ") != 1 || last.Text != got {
		t.Fatalf("before the restart %q, in all %q, end %q", seen, got, last.Text)
	}
}

func TestASteerBetweenRoundsStillStreamsTheNextRound(t *testing.T) {
	c, id, _ := thread(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	ch, err := c.Prompt(ctx, id, "slowtool")
	if err != nil {
		t.Fatal(err)
	}
	var after []time.Time
	var events []chat.Event
	ended := false
	for e := range ch {
		events = append(events, e)
		switch {
		case e.Kind == chat.ToolStart:
			if _, err := c.Prompt(ctx, id, "also say hello"); err != nil {
				t.Fatal(err)
			}
		case e.Kind == chat.ToolEnd:
			ended = true
		case e.Kind == chat.Delta && ended:
			after = append(after, time.Now())
		}
	}
	if got := replyText(events); !strings.HasSuffix(got, "After the tool come eight more words here.") {
		t.Fatalf("text %q (%s)", got, kinds(events))
	}
	if len(after) < 4 || after[len(after)-1].Sub(after[0]) < 200*time.Millisecond {
		t.Fatalf("the round after the steer came in %d deltas: %s", len(after), kinds(events))
	}
}

func TestResumingAnOlderFinishedTurnEndsWithIt(t *testing.T) {
	c, id, d := thread(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	var first chat.Cursor
	for i, p := range []string{"words 2 0", "words 3 0", "words 4 0"} {
		ch, err := c.Prompt(ctx, id, p)
		if err != nil {
			t.Fatal(err)
		}
		events := collect(t, ch, 30*time.Second)
		if i == 0 {
			first = events[0].Cursor
		}
	}
	other := chat.New(d.endpoint())
	defer other.Close()
	rest, err := other.Resume(ctx, id, first)
	if err != nil {
		t.Fatal(err)
	}
	after := collect(t, rest, 30*time.Second)
	if replyText(after) != " w1" || after[len(after)-1].Kind != chat.End || after[len(after)-1].Text != "w0 w1" {
		t.Fatalf("events %s, text %q, last %+v", kinds(after), replyText(after), after[len(after)-1])
	}
}

func TestResumingATurnFarBehindTheTailReplaysIt(t *testing.T) {
	c, id, d := thread(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	var first chat.Cursor
	for i := range 7 {
		ch, err := c.Prompt(ctx, id, "words 2 0")
		if err != nil {
			t.Fatal(err)
		}
		events := collect(t, ch, 30*time.Second)
		if i == 0 {
			first = events[0].Cursor
		}
	}
	for range 5 {
		other := chat.New(d.endpoint())
		rest, err := other.Resume(ctx, id, first)
		if err != nil {
			t.Fatal(err)
		}
		after := collect(t, rest, 30*time.Second)
		other.Close()
		if replyText(after) != " w1" || after[len(after)-1].Text != "w0 w1" {
			t.Fatalf("events %s, text %q", kinds(after), replyText(after))
		}
	}
}

func TestADaemonKilledMidReplyNeverTakesShownTextBack(t *testing.T) {
	c, id, d := thread(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	ch, err := c.Prompt(ctx, id, "words 100 50")
	if err != nil {
		t.Fatal(err)
	}
	var events []chat.Event
	for e := range ch {
		events = append(events, e)
		if len(events) == 6 {
			break
		}
	}
	seen := replyText(events)
	d.kill(t)
	d.must(t, "ls")
	events = append(events, collect(t, ch, time.Minute)...)
	last := events[len(events)-1]
	got := replyText(events)
	if last.Kind != chat.End || !strings.HasPrefix(got, seen) || last.Text != got {
		t.Fatalf("shown before the kill %q\nin all %q\nend %+v", seen, got, last)
	}
}
