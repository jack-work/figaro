package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/aria"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/api/rpc"
)

type fakeLogReads struct {
	msgs  []message.Message
	reads []rpc.ReadRequest
}

func (f *fakeLogReads) lt(turn uint64) uint64 {
	for i, m := range f.msgs {
		if m.TurnID == turn {
			return uint64(i + 1)
		}
	}
	return 0
}

func (f *fakeLogReads) Read(_ context.Context, req rpc.ReadRequest) (aria.Page, error) {
	f.reads = append(f.reads, req)
	var p aria.Page
	start := req.At.Turn
	if start == 0 {
		start = 1
	}
	for turn := start; turn <= uint64(len(f.msgs)); turn++ {
		if req.Until.Turn > 0 && turn >= req.Until.Turn {
			p.More.After = true
			p.Next = &aria.Anchor{Turn: turn}
			return p, nil
		}
		if len(p.Parts) == 2 {
			p.More.After, p.Next = true, &aria.Anchor{Turn: turn}
			return p, nil
		}
		lt := f.lt(turn)
		p.Parts = append(p.Parts, aria.TurnPart{Turn: aria.Turn{ID: turn, LTs: []uint64{lt, lt}}})
	}
	return p, nil
}

func (f *fakeLogReads) IRRange(_ context.Context, _ string, from, before uint64) (*rpc.IRResponse, error) {
	out := &rpc.IRResponse{}
	for lt := from; lt < before && lt <= uint64(len(f.msgs)); lt++ {
		raw, _ := json.Marshal(f.msgs[lt-1])
		out.Entries = append(out.Entries, rpc.IREntry{LT: lt, Payload: raw})
		if len(out.Entries) == 2 && lt+1 < before {
			out.NextFrom = lt + 1
			return out, nil
		}
	}
	return out, nil
}

func entryMsg(turn uint64, text string) message.Message {
	c := message.TextContent(text)
	c.Principal = "user:clerk"
	return message.Message{Role: message.RoleInput, TurnID: turn, Timestamp: int64(1000 + turn), Content: []message.Content{c}}
}

func TestReadPrintsARangeOfEntriesExactly(t *testing.T) {
	texts := []string{"one\n", "<system-reminder name=\"law\">two</system-reminder>\n\n", "three & <b>", "  four  ", "five"}
	f := &fakeLogReads{}
	for i, text := range texts {
		f.msgs = append(f.msgs, entryMsg(uint64(i+1), text))
	}
	entries, err := readEntries(context.Background(), f, "log", 2, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 {
		t.Fatalf("[2,5) read %d entries: %+v", len(entries), entries)
	}
	var out bytes.Buffer
	if err := writeEntries(&out, entries, true); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
	for i, line := range lines {
		var e logEntry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Fatal(err)
		}
		want := uint64(i + 2)
		if e.Turn != want || e.Text != texts[want-1] || e.Principal != "user:clerk" || e.At != int64(1000+want) || e.LT != want {
			t.Fatalf("entry %d = %+v, want turn %d with its exact text %q", i, e, want, texts[want-1])
		}
	}
	for _, r := range f.reads {
		if r.Until.Turn != 5 {
			t.Fatalf("a read did not carry the ceiling: %+v", r)
		}
	}

	var plain bytes.Buffer
	_ = writeEntries(&plain, entries, false)
	if strings.Contains(plain.String(), "<system-reminder") {
		t.Fatalf("the reading view printed a live tag:\n%s", plain.String())
	}
}
