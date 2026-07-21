package cli

import (
	"fmt"
	"runtime/debug"
	"testing"
	"time"

	"github.com/jack-work/figaro/internal/livedoc"
	"github.com/jack-work/figaro/internal/livelog/aria"
	ldrender "github.com/jack-work/figaro/internal/livelog/render"
)

// fzWorld drives a transcript against a scripted aria conversation: the full
// history is served page-by-page (the same ReadBefore contract the stream
// loop speaks), one message streams open, and mutations (commits, live
// deltas, resizes, leave/enter) interleave with keys.
type fzWorld struct {
	tr      *transcript
	client  *aria.Client
	history []aria.Committed
	openLT  int
	openV   int
	seq     int
}

func fzMessage(lt int) aria.Committed {
	role := "assistant"
	if lt%3 == 1 {
		role = "user"
	}
	return aria.Committed{LT: lt, Role: role, Nodes: []livedoc.Node{{
		Type:     livedoc.NodeProse,
		Markdown: fmt.Sprintf("Msg%03d body **bold** `code` héllo 漢字 x%d", lt, lt),
	}}}
}

func newFzWorldHistory(history []aria.Committed, w, h int, view ldrender.NodeView) *fzWorld {
	wl := &fzWorld{history: history, openLT: history[len(history)-1].LT + 1}
	wl.client = aria.NewClient()
	wl.client.SetClosedLimit(transcriptTailLimit)
	wl.client.Apply(readBefore(history, recentCursor, transcriptPageSize))
	wl.client.Apply(aria.AriaRead{Live: &aria.Live{
		LT: wl.openLT, V: 0, Role: "assistant",
		Nodes: []aria.NodeDelta{{ID: "n0", Set: map[string]any{
			"type": "prose", "markdown": "streaming aaaa body live",
		}}},
	}})
	ft := ldrender.NewFakeTerminal(w, h)
	wl.tr = newTranscript(ft, w, h, view, wl.client, "fuzzaria", time.Now())
	wl.client.OnClosed = func(m aria.Message) {
		wl.tr.observeCommitted(m)
		if wl.tr.active {
			wl.tr.render()
		}
	}
	wl.client.OnLive = func(int, string, []livedoc.Node) {
		if wl.tr.active {
			wl.tr.render()
		}
	}
	wl.tr.enter()
	return wl
}

func newFzWorld(committed, w, h int) *fzWorld {
	history := make([]aria.Committed, committed)
	for i := range history {
		history[i] = fzMessage(i + 1)
	}
	return newFzWorldHistory(history, w, h, ldrender.NodeText{})
}

// commitOne closes the open message with a full snapshot and opens a new live
// message right after it, as a turn boundary does.
func (w *fzWorld) commitOne() {
	w.seq++
	msg := fzMessage(w.openLT)
	w.history = append(w.history, msg)
	w.client.Apply(aria.AriaRead{Committed: []aria.Committed{msg}})
	w.openLT++
	w.openV = 0
	w.client.Apply(aria.AriaRead{Live: &aria.Live{
		LT: w.openLT, V: 0, Role: "assistant",
		Nodes: []aria.NodeDelta{{ID: "n0", Set: map[string]any{
			"type": "prose", "markdown": fmt.Sprintf("streaming-%03d aaaa ", w.openLT),
		}}},
	}})
}

// liveDelta appends a splice to the open message's streamed markdown.
func (w *fzWorld) liveDelta() {
	w.seq++
	w.openV++
	w.client.Apply(aria.AriaRead{Live: &aria.Live{
		LT: w.openLT, V: w.openV,
		Nodes: []aria.NodeDelta{{ID: "n0", Patch: map[string]livedoc.Delta{
			"markdown": {At: 1 << 30, Ins: fmt.Sprintf("δ%d wide漢 ", w.seq)},
		}}},
	}})
}

// servePage answers one transcriptPageRequest from the scripted history,
// mirroring readTranscriptPage: cached payloads win, after/watermark requests
// page forward, before requests replay via keyset ReadBefore.
func (w *fzWorld) servePage(req transcriptPageRequest) []aria.Message {
	if len(req.cached) > 0 {
		return req.cached
	}
	if req.after != 0 {
		var out []aria.Message
		for _, c := range w.history {
			if c.LT <= req.after {
				continue
			}
			out = append(out, aria.Message{LT: c.LT, Role: c.Role, Nodes: c.Nodes})
			if len(out) == transcriptPageSize {
				break
			}
		}
		return out
	}
	limit := transcriptPageSize
	if req.expected.Count != 0 {
		limit = req.expected.Count
	}
	return committedMessages(readBefore(w.history, req.before, limit).Committed)
}

// servePages drains pending page requests, bounded like the stream's search
// worker so an armed paged scan can never spin the harness forever.
func (w *fzWorld) servePages() {
	for range 64 {
		req, ok := w.tr.pageCursor()
		if !ok {
			return
		}
		w.tr.applyPage(req, w.servePage(req))
	}
}

// checkCheap asserts the field-level invariants (no rendering).
func (w *fzWorld) checkCheap(t testing.TB, ctx string) {
	tr := w.tr
	if !tr.active {
		t.Fatalf("%s: tr.active cleared — only leave() may do that, key() must never", ctx)
	}
	if tr.offset < 0 {
		t.Fatalf("%s: tr.offset = %d, must stay >= 0", ctx, tr.offset)
	}
	switch tr.vmode {
	case visualOff, visualCursor, visualChar, visualLine, visualColumn:
	default:
		t.Fatalf("%s: tr.vmode = %d, not a defined visualMode", ctx, tr.vmode)
	}
}

// checkInvariants asserts the full structural invariants. It is deliberately
// state-preserving: the double-yank probe restores the visual mode it
// collapses (the endpoints are LT-anchored and re-resolve).
func (w *fzWorld) checkInvariants(t testing.TB, ctx string) {
	w.checkCheap(t, ctx)
	tr := w.tr
	all := tr.lines()
	if len(all) != len(tr.lineLT) {
		t.Fatalf("%s: lines() has %d rows but lineLT has %d", ctx, len(all), len(tr.lineLT))
	}
	if tr.hasCursor && len(all) > 0 {
		row, _ := tr.pointToRow(tr.vCursor)
		if row < 0 || row >= len(all) {
			t.Fatalf("%s: pointToRow(vCursor) = %d, out of bounds for %d rows", ctx, row, len(all))
		}
		cRow, cCol, _ := tr.resolveCursor()
		if cRow < 0 || cRow >= len(all) {
			t.Fatalf("%s: resolveCursor row = %d, out of bounds for %d rows", ctx, cRow, len(all))
		}
		if cCol < 0 {
			t.Fatalf("%s: resolveCursor col = %d, negative", ctx, cCol)
		}
	}
	saved := tr.vmode
	_, _ = tr.visualYankText()
	if _, ok := tr.visualYankText(); ok {
		t.Fatalf("%s: second consecutive visualYankText returned ok=true", ctx)
	}
	tr.vmode = saved
}

// FuzzTranscriptKeys feeds arbitrary byte sequences to the transcript pager's
// full key surface, interleaved with model mutations (new commits, live-frame
// deltas, resizes, leave/enter) and page servicing, asserting the structural
// invariants after every byte. Any panic is converted into a t.Fatalf carrying
// the input and the step that blew up.
func FuzzTranscriptKeys(f *testing.F) {
	seeds := []string{
		"/msg0[45]\r" + "nvjjy",
		"ivwlly",
		"?abc\rnN",
		"&foo\r&\r",
		"\x1bq\x16Vjjy",
		"gg G",
		"jjjjkkkkuuddGgg",
		"/(a+)+$\rnNnN",
		"ivl$0hhjkV\rV\x16v",
		"!?!q\x1b!!",
		"/\x7f\x7fab\x7fx\rn&x\r\x1b\x1b",
		"\x00\x01j\x02k\x03\x04g\x05g\x1fG\x0e\x10\x0d\x16\x0f",
		"i/body\rnnnNNvG$y",
		"kkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkk",
		"&\r/\r?\x1b",
		"V\x1b\x1bv\x1bi\x1bq",
		"g/never-matches-anything\rnN\x1b",
	}
	for _, s := range seeds {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 512 {
			data = data[:512]
		}
		w := newFzWorld(12, 60, 12)
		step := func(i int, desc string, fn func()) {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("panic at byte %d (%s) of input %q: %v\n%s",
						i, desc, data, r, debug.Stack())
				}
			}()
			fn()
		}
		for i, b := range data {
			if (int(b)+i)%4 == 0 { // 1-in-4: mutate first, kind from the byte's low bits
				switch (b >> 2) & 3 {
				case 0:
					step(i, "mutate:commit", w.commitOne)
				case 1:
					step(i, "mutate:live-delta", w.liveDelta)
				case 2:
					rw := 20 + (int(b)*7+i)%101
					rh := 5 + (int(b)*13+i)%36
					step(i, fmt.Sprintf("mutate:resize %dx%d", rw, rh), func() { w.tr.resize(rw, rh) })
				case 3:
					step(i, "mutate:leave+enter", func() { w.tr.leave(); w.tr.enter() })
				}
			}
			step(i, fmt.Sprintf("key %q", b), func() { w.tr.key(b) })
			step(i, "page-service", w.servePages)
			step(i, "invariants", func() {
				w.checkInvariants(t, fmt.Sprintf("after byte %d (%q) of input %q", i, b, data))
			})
		}
	})
}
