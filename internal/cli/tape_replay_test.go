package cli

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jack-work/figaro/internal/livedoc"
	"github.com/jack-work/figaro/internal/livelog/aria"
	ldrender "github.com/jack-work/figaro/internal/livelog/render"
	"github.com/jack-work/figaro/internal/rpc"
	"github.com/jack-work/figaro/internal/tape"
)

var tapeFlag = flag.String("tape", "", "replay this tape instead of testdata/tapes/*.tape")

// ---------------------------------------------------------------------------
// Tape-driven regression tests.
//
// A tape is a recording of what an agent actually said and when. Replaying one
// through the real renderer turns "the range row wavered on my screen at 15:41"
// into an assertion a machine can make, forever, with no daemon, no provider
// and no tokens.
//
// This is the HEADLESS half of `figaro replay`: same pages, same renderer, a
// fake terminal instead of a pty. The pty half is for eyes (and for the
// escape-level defects only a real terminal shows); this half is for CI.
// ---------------------------------------------------------------------------

// tapePages extracts the live-render pages from a tape in wire order: the
// catch-up read's response first (it is the seed the pager opens on), then
// every pushed frame.
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
		switch f.Method() {
		case rpc.MethodAriaFrame:
			var m struct {
				Params aria.Page `json:"params"`
			}
			if json.Unmarshal(f.Msg, &m) == nil {
				pages = append(pages, m.Params)
			}
		case "": // a response: only figaro.read carries a page
			var m struct {
				Result *aria.Page `json:"result"`
			}
			if json.Unmarshal(f.Msg, &m) == nil && m.Result != nil && len(m.Result.Parts) > 0 {
				pages = append(pages, *m.Result)
			}
		}
	}
	return h, pages
}

func tapePaths(t *testing.T) []string {
	t.Helper()
	if *tapeFlag != "" {
		return []string{*tapeFlag}
	}
	paths, _ := filepath.Glob(filepath.Join("testdata", "tapes", "*.tape"))
	return paths
}

// TestTapeReplayKeepsTheWindowHonest is the regression gate the wavering
// range row asked for.
//
// THE INVARIANT: while the pager is FOLLOWING, the transcript's row total may
// only grow. Following means the window is anchored at the newest message and
// history is only ever added behind it, so a total that falls has dropped rows
// the reader can still scroll to — and a total that falls and rises repeatedly
// is the pager arguing with itself about how much it holds. That is exactly
// what the master saw on 2026-08-01: a range row alternating between
// 1043–1072/1072+ and 546–575/575+ for a whole turn, on one aria, at one
// width, with the painted body unchanged.
//
// It is asserted from a TAPE rather than from a fixture because the defect
// needs a real aria's shape to appear: one message taller than the window
// tuner's hysteresis band. No fixture anybody wrote by hand had one; the
// recording of a real session does.
func TestTapeReplayKeepsTheWindowHonest(t *testing.T) {
	paths := tapePaths(t)
	if len(paths) == 0 {
		t.Skip("no tapes: put one in internal/cli/testdata/tapes/ or pass -tape")
	}
	for _, path := range paths {
		t.Run(filepath.Base(path), func(t *testing.T) {
			h, pages := tapePages(t, path)
			if len(pages) == 0 {
				t.Skipf("%s carries no aria pages", filepath.Base(path))
			}
			cols, rows := h.Cols, h.Rows
			if cols <= 0 || rows < 8 {
				cols, rows = 100, 33
			}
			client := aria.NewClient()
			client.SetClosedLimit(transcriptTailLimit)
			tr := newTranscript(ldrender.NewFakeTerminal(cols, rows), cols, rows,
				ldrender.NodeText{}, client, h.Aria, time.Time{})
			tr.enter()

			var (
				prev    int
				drops   int
				firstAt int
			)
			for i, p := range pages {
				client.Apply(p)
				tr.renderFrame()
				total := tr.index.total
				if !tr.follow {
					continue
				}
				if total < prev {
					if drops == 0 {
						firstAt = i
					}
					drops++
				}
				prev = total
			}
			if drops > 0 {
				t.Errorf("the retained window shrank %d times while following "+
					"(first at page %d of %d): the pager's row total is not stable, "+
					"so the range row wavers. See tuneTail in transcript.go.",
					drops, firstAt, len(pages))
			}
		})
	}
}

// TestTapeReplayIsDeterministic: the same tape, replayed twice, paints the
// same rows. Without this the golden below is worth nothing — and it is not
// free, because the renderer reads a clock in three places and would happily
// paint a different spinner phase or a different timestamp on the second pass.
func TestTapeReplayIsDeterministic(t *testing.T) {
	paths := tapePaths(t)
	if len(paths) == 0 {
		t.Skip("no tapes")
	}
	run := func(path string) []string {
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
		var out []string
		for _, p := range pages {
			client.Apply(p)
			tr.renderFrame()
			body, _ := tr.layout(len(tr.footLines()))
			rule, _ := tr.footerRows(tr.index.total, body)
			out = append(out, rule)
		}
		return out
	}
	for _, path := range paths {
		t.Run(filepath.Base(path), func(t *testing.T) {
			a, b := run(path), run(path)
			if len(a) != len(b) {
				t.Fatalf("frame counts differ: %d vs %d", len(a), len(b))
			}
			for i := range a {
				if a[i] != b[i] {
					t.Fatalf("frame %d differs between runs:\n A %q\n B %q", i, a[i], b[i])
				}
			}
		})
	}
}

// TestTapeIsReadableAndSelfDescribing guards the fixtures themselves: a tape
// that has rotted (truncated, half-written, from a future format) must fail
// loudly here rather than silently skipping every test above.
func TestTapeIsReadableAndSelfDescribing(t *testing.T) {
	for _, path := range tapePaths(t) {
		h, frames, err := tape.Read(path)
		if err != nil {
			t.Errorf("%s: %v", filepath.Base(path), err)
			continue
		}
		if h.Aria == "" {
			t.Errorf("%s: header names no aria", filepath.Base(path))
		}
		if _, err := time.Parse(time.RFC3339Nano, h.Started); err != nil {
			t.Errorf("%s: header start time %q: %v", filepath.Base(path), h.Started, err)
		}
		if len(frames) == 0 {
			t.Errorf("%s: no frames", filepath.Base(path))
		}
		if fi, err := os.Stat(path); err == nil && fi.Size() > 8<<20 {
			t.Logf("%s is %d MB — large for a committed fixture; consider trimming it",
				filepath.Base(path), fi.Size()>>20)
		}
	}
}

// ---------------------------------------------------------------------------
// Minting a tape from a fixture
// ---------------------------------------------------------------------------

var mintTape = flag.String("mint-tape", "", "write a synthetic tape to this path and exit")

// TestMintTapeFromFixture writes a tape by hand — the OTHER direction of the
// same road. A recording turns a bug someone saw into a test; this turns a
// bug someone reasoned about into something a human can watch in a terminal
// (`figaro replay`), which is how you find out whether the reasoning was right.
//
// The fixture is the shape that breaks the window tuner: short messages with
// one tool dump taller than its hysteresis band, then a turn that streams.
// It is inert unless -mint-tape names a path.
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

	push := func(p aria.Page) {
		params, err := json.Marshal(p)
		if err != nil {
			t.Fatal(err)
		}
		msg, err := json.Marshal(map[string]any{
			"jsonrpc": "2.0", "method": rpc.MethodAriaFrame, "params": json.RawMessage(params),
		})
		if err != nil {
			t.Fatal(err)
		}
		w.Frame(tape.In, msg)
	}

	hist := spikeHistory(120, 2, 500, 11)
	push(aria.Page{Parts: hist})
	stream := ""
	for f := range 40 {
		stream += "thinking line " + itoa(f) + " of a turn that streams for a while\n\n"
		push(aria.Page{Parts: []aria.TurnPart{{Turn: aria.Turn{ID: 121, Live: &aria.Live{
			From: 0, V: f,
			Nodes: []aria.NodeDelta{{ID: 0, Set: map[string]any{"type": "thinking", "markdown": stream}}},
		}}}}})
		time.Sleep(12 * time.Millisecond) // real intervals: the tape is a schedule
	}
	t.Logf("wrote %s", *mintTape)
}

// spikeHistory is the fixture shape that breaks the tail-window tuner: short
// messages everywhere, with ONE giant message (a big tool dump) sitting
// `depth` messages back from the tail. tuneTail sizes the window from the
// window's own average height, so a message taller than the hysteresis band
// between its grow and shrink thresholds gives the loop no fixed point.
func spikeHistory(n, shortLines, giantLines, depth int) []aria.TurnPart {
	out := make([]aria.TurnPart, n)
	for i := range out {
		lines := shortLines
		if i == n-depth {
			lines = giantLines
		}
		md := ""
		for l := range lines {
			md += "message-" + itoa(i+1) + " line-" + itoa(l) + "\n\n"
		}
		out[i] = aria.TurnPart{Turn: aria.Turn{ID: uint64(i + 1), Sealed: true,
			Nodes: []livedoc.Node{{Type: livedoc.NodeProse, Markdown: md}}}}
	}
	return out
}
