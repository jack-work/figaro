package cli

import (
	"encoding/json"
	"flag"
	"path/filepath"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/rpc"
	ldrender "github.com/jack-work/figaro/internal/livelog/render"
	"github.com/jack-work/figaro/internal/tape"
	"github.com/jack-work/figaro/sdk/aria"
)

var (
	tapeFlag = flag.String("tape", "", "replay this tape instead of testdata/tapes/*.tape")
	mintTape = flag.String("mint-tape", "", "write a synthetic tape to this path and exit")
)

// tapePaths: the tape under -tape, or every committed fixture.
func tapePaths(t *testing.T) []string {
	t.Helper()
	if *tapeFlag != "" {
		return []string{*tapeFlag}
	}
	paths, _ := filepath.Glob(filepath.Join("testdata", "tapes", "*.tape"))
	if len(paths) == 0 {
		t.Skip("no tapes in testdata/tapes/, and no -tape given")
	}
	return paths
}

// tapePages: the catch-up read's response, then every push, in wire order.
func tapePages(t *testing.T, path string) (tape.Header, []aria.Page) {
	t.Helper()
	h, frames, err := tape.Read(path)
	if err != nil {
		t.Fatalf("%s: %v", filepath.Base(path), err)
	}
	var pages []aria.Page
	for _, f := range frames {
		if f.Dir != tape.In {
			continue
		}
		var m struct {
			Params aria.Page  `json:"params"`
			Result *aria.Page `json:"result"`
		}
		if json.Unmarshal(f.Msg, &m) != nil {
			continue
		}
		switch {
		case f.Method() == rpc.MethodAriaFrame:
			pages = append(pages, m.Params)
		case f.Method() == "" && m.Result != nil && len(m.Result.Parts) > 0:
			pages = append(pages, *m.Result)
		}
	}
	if len(pages) == 0 {
		t.Skipf("%s carries no aria pages", filepath.Base(path))
	}
	return h, pages
}

// replayTape renders every page at the recorded geometry, calling frame after each.
func replayTape(t *testing.T, path string, frame func(tr *transcript, i int)) {
	t.Helper()
	h, pages := tapePages(t, path)
	cols, rows := 100, 33
	if h.Cols > 0 && h.Rows >= 8 {
		cols, rows = h.Cols, h.Rows
	}
	client := aria.NewClient()
	client.SetClosedLimit(transcriptTailLimit)
	tr := newTranscript(ldrender.NewFakeTerminal(cols, rows), cols, rows,
		ldrender.NodeText{}, client, h.Aria, time.Time{})
	tr.enter()
	for i, p := range pages {
		client.Apply(p, aria.Notify)
		tr.renderFrame()
		frame(tr, i)
	}
}

// While following, the retained row total may only grow: history is only
// added behind the anchored window.
func TestTapeReplayKeepsTheWindowHonest(t *testing.T) {
	for _, path := range tapePaths(t) {
		t.Run(filepath.Base(path), func(t *testing.T) {
			prev := 0
			replayTape(t, path, func(tr *transcript, i int) {
				if tr.follow && tr.index.total < prev {
					t.Errorf("page %d: the window shrank %d -> %d while following; "+
						"the row total is not stable, so the range row wavers",
						i, prev, tr.index.total)
				}
				prev = tr.index.total
			})
		})
	}
}

// The renderer reads a clock in three places; same tape must give same rows.
func TestTapeReplayIsDeterministic(t *testing.T) {
	rules := func(path string) []string {
		var out []string
		replayTape(t, path, func(tr *transcript, _ int) {
			body, _ := tr.layout(len(tr.footLines()))
			rule, _ := tr.footerRows(tr.index.total, body)
			out = append(out, rule)
		})
		return out
	}
	for _, path := range tapePaths(t) {
		t.Run(filepath.Base(path), func(t *testing.T) {
			a, b := rules(path), rules(path)
			for i := range max(len(a), len(b)) {
				if i >= len(a) || i >= len(b) || a[i] != b[i] {
					t.Fatalf("frame %d differs between runs of one tape", i)
				}
			}
		})
	}
}

// spikeHistory is tallHistory with one giant message `depth` back from the tail.
func spikeHistory(n, shortLines, giantLines, depth int) []aria.TurnPart {
	out, tall := tallHistory(n, shortLines), tallHistory(n, giantLines)
	out[n-depth] = tall[n-depth]
	return out
}

// Inert without -mint-tape.
func TestMintTapeFromFixture(t *testing.T) {
	if *mintTape == "" {
		t.Skip("pass -mint-tape <path> to write one")
	}
	w, err := tape.Create(*mintTape, tape.Header{
		Aria: "spikefix", Cols: 100, Rows: 33, Term: "xterm-256color",
		Note: "synthetic: short messages + one 500-row dump 11 back, then a streaming turn",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	mustJSON := func(v any) json.RawMessage {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	push := func(p aria.Page) {
		w.Frame(tape.In, mustJSON(map[string]any{
			"jsonrpc": "2.0", "method": rpc.MethodAriaFrame, "params": mustJSON(p),
		}))
	}

	push(aria.Page{Parts: spikeHistory(120, 2, 500, 11)})
	stream := ""
	for f := range 40 {
		stream += "thinking line " + itoa(f) + " of a turn that streams for a while\n\n"
		push(aria.Page{Parts: []aria.TurnPart{{Turn: aria.Turn{ID: 121, Live: &aria.Live{
			From: 0, V: f,
			Nodes: []aria.NodeDelta{{ID: 0, Set: map[string]any{"type": "thinking", "markdown": stream}}},
		}}}}})
		time.Sleep(12 * time.Millisecond)
	}
	t.Logf("wrote %s", *mintTape)
}
