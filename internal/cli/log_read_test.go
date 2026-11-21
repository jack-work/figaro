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
	part := func(turn uint64) aria.TurnPart {
		lt := f.lt(turn)
		return aria.TurnPart{Turn: aria.Turn{ID: turn, LTs: []uint64{lt, lt}}}
	}
	if req.Backward {
		if t := req.At.Turn; t > 1 {
			p.Parts = append(p.Parts, part(t-1))
		}
		return p, nil
	}
	if t := req.At.Turn; t >= 1 && t <= uint64(len(f.msgs)) {
		p.Parts = append(p.Parts, part(t))
	}
	return p, nil
}

func (f *fakeLogReads) IRFrom(_ context.Context, _ string, from uint64) (*rpc.IRResponse, error) {
	out := &rpc.IRResponse{}
	before := uint64(len(f.msgs)) + 1
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

	var plain bytes.Buffer
	_ = writeEntries(&plain, entries, false)
	if strings.Contains(plain.String(), "<system-reminder") {
		t.Fatalf("the reading view printed a live tag:\n%s", plain.String())
	}
}

func TestReadKeepsAnEmptyEntryAtEitherEdge(t *testing.T) {
	f := &fakeLogReads{}
	for i, text := range []string{"one", "", "three", ""} {
		f.msgs = append(f.msgs, entryMsg(uint64(i+1), text))
	}
	for _, c := range []struct{ from, until uint64 }{{2, 4}, {2, 0}, {1, 3}, {4, 0}} {
		entries, err := readEntries(context.Background(), f, "log", c.from, c.until)
		if err != nil {
			t.Fatal(err)
		}
		last := uint64(len(f.msgs)) + 1
		if c.until > 0 {
			last = c.until
		}
		if uint64(len(entries)) != last-c.from {
			t.Fatalf("[%d,%d) read %+v", c.from, c.until, entries)
		}
		for i, e := range entries {
			if e.Turn != c.from+uint64(i) {
				t.Fatalf("[%d,%d) entry %d is turn %d", c.from, c.until, i, e.Turn)
			}
		}
	}
}
