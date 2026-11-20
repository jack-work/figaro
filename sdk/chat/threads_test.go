package chat_test

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/jack-work/figaro/sdk/chat"
)

func threadIDs(p chat.ThreadPage) []string {
	var out []string
	for _, th := range p.Threads {
		out = append(out, th.ID)
	}
	slices.Sort(out)
	return out
}

func TestThreadsListTheArias(t *testing.T) {
	d := newDaemon(t)
	root := d.root(t)
	c := chat.New(d.endpoint())
	t.Cleanup(func() { c.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	talked, err := c.Open(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	quiet, err := c.Open(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	d.must(t, "set", "--id", quiet, "mantra", "Quiet Harbour")
	ch, err := c.Prompt(ctx, talked, "words 2 0")
	if err != nil {
		t.Fatal(err)
	}
	collect(t, ch, 30*time.Second)
	h, err := c.History(ctx, talked, 0, 1)
	if err != nil || len(h.Turns) != 1 {
		t.Fatalf("history %+v: %v", h, err)
	}

	all, err := c.Threads(ctx, chat.ThreadFilter{})
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]chat.Thread{}
	for _, th := range all.Threads {
		by[th.ID] = th
	}
	got := by[talked]
	if got.Turn != h.Turns[0].ID || got.Parent != root || got.State != chat.StateLive || got.Depth != 1 || time.Since(got.At) > time.Minute || got.At.IsZero() {
		t.Fatalf("talked thread %+v, want turn %d under %s, live, depth 1, recent", got, h.Turns[0].ID, root)
	}
	if q := by[quiet]; q.Turn != by[root].Turn || q.Turn == 0 || q.Turn >= got.Turn || q.Mantra != "Quiet Harbour" {
		t.Fatalf("quiet thread %+v: a fork shows the turn it inherited, root %+v", q, by[root])
	}
	if by[root].Depth != 0 || by[root].Parent != "" {
		t.Fatalf("root %+v", by[root])
	}

	again, err := c.Threads(ctx, chat.ThreadFilter{Match: "harbour"})
	if err != nil || len(again.Threads) != 1 || again.Threads[0].State != chat.StateDormant || by[quiet].State != chat.StateDormant {
		t.Fatalf("listing woke the quiet aria: before %q, after %+v, %v", by[quiet].State, again.Threads, err)
	}

	under, err := c.Threads(ctx, chat.ThreadFilter{Under: root})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{quiet, talked}; !slices.Equal(threadIDs(under), slices.Sorted(slices.Values(want))) {
		t.Fatalf("under root: %v, want %v", threadIDs(under), want)
	}
	match, err := c.Threads(ctx, chat.ThreadFilter{Match: "harbour"})
	if err != nil || !slices.Equal(threadIDs(match), []string{quiet}) {
		t.Fatalf("match harbour: %v, %v", threadIDs(match), err)
	}
	holders, err := c.Threads(ctx, chat.ThreadFilter{Holders: true, Under: root})
	if err != nil || len(holders.Threads) != 0 {
		t.Fatalf("holders under root: %+v, %v", holders, err)
	}

	var paged []string
	f := chat.ThreadFilter{Limit: 1}
	for range 10 {
		p, err := c.Threads(ctx, f)
		if err != nil {
			t.Fatal(err)
		}
		if len(p.Threads) > 1 {
			t.Fatalf("a page of limit 1 holds %d threads", len(p.Threads))
		}
		paged = append(paged, threadIDs(p)...)
		if p.Next == "" {
			break
		}
		f.Before = p.Next
	}
	slices.Sort(paged)
	if !slices.Equal(paged, threadIDs(all)) {
		t.Fatalf("paged %v, whole %v", paged, threadIDs(all))
	}
}
