package livelog_test

import (
	"strings"
	"testing"

	"github.com/jack-work/figaro/internal/livelog"
	"github.com/jack-work/figaro/internal/livelog/doc"
	"github.com/jack-work/figaro/internal/livelog/render"
	"github.com/jack-work/figaro/internal/livelog/stream"
)

func patch(s *stream.Server, id, old, new string) {
	d, _ := doc.Diff(old, new)
	s.Publish(doc.Patch(id, d))
}

const spinners = "⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏"

// TestE2E_FullPipeline exercises every layer together: a fresh viewer catches up
// pre-existing history via the paginated snapshot, follows a live thinking →
// write → running-bash stream, survives a mid-run viewport shrink, then
// disconnects, lets the server move on, and reconnects to recover the gap.
func TestE2E_FullPipeline(t *testing.T) {
	s := stream.NewServer()

	// Pre-existing history — the fresh viewer must reconstruct it from the snapshot.
	s.Publish(doc.Append(doc.Block{ID: "intro", Kind: "text"}))
	patch(s, "intro", "", "Welcome to the log.")

	ft := render.NewFakeTerminal(72, 24)
	v := livelog.NewViewer(ft, render.TextRenderer{}, 8)
	disconnect := v.Connect(s)

	if !strings.Contains(strings.Join(ft.Screen(), "\n"), "Welcome to the log.") {
		t.Fatalf("catch-up missed history:\n%s", strings.Join(ft.Screen(), "\n"))
	}

	// Live stream: thinking, a completed write, then a still-running bash tool.
	s.Publish(doc.Append(doc.Block{ID: "th", Kind: "thinking"}))
	patch(s, "th", "", "Planning the script.")
	s.Publish(doc.Append(doc.Block{ID: "w", Kind: "write", Status: doc.StatusOK,
		Attrs: map[string]string{"title": "S.sh"}}))
	patch(s, "w", "", "Wrote 196 bytes to S.sh")
	s.Publish(doc.Append(doc.Block{ID: "b", Kind: "bash", Status: doc.StatusActive,
		Attrs: map[string]string{"title": "chmod && cat"}}))
	patch(s, "b", "", "#!/usr/bin/env bash\nl1\nl2\nl3\nl4\nl5\nl6")

	// SIGWINCH mid-run: the pane shrinks (non-focused / relaid-out).
	ft.Resize(72, 8)
	v.Tick() // any redraw reconciles to the new size
	s.Publish(doc.SetStatus("b", doc.StatusOK))

	scr := strings.Join(ft.Screen(), "\n")
	if strings.Count(scr, "bash chmod && cat") != 1 {
		t.Fatalf("bash header duplicated across resize:\n%s", scr)
	}
	if strings.ContainsAny(scr, spinners) {
		t.Fatalf("stranded spinner after resize+complete:\n%s", scr)
	}

	// Disconnect; the server keeps working while the viewer is away.
	disconnect()
	s.Publish(doc.Append(doc.Block{ID: "c", Kind: "text"}))
	patch(s, "c", "", "After reconnect.")
	patch(s, "th", "Planning the script.", "Planning the script. Done.")

	// Reconnect: recover the gap from the tail, resume live.
	v.Reconnect(s)
	scr = strings.Join(ft.Screen(), "\n")
	if !strings.Contains(scr, "After reconnect.") {
		t.Fatalf("reconnect missed the gap:\n%s", scr)
	}
	if !strings.Contains(scr, "Planning the script. Done.") {
		t.Fatalf("reconnect missed an in-place edit:\n%s", scr)
	}

	// The viewer's document must equal the server's, exactly.
	got, want := v.Client().Doc().Blocks(), s.Blocks()
	if len(got) != len(want) {
		t.Fatalf("convergence: %d blocks vs %d", len(got), len(want))
	}
	for i := range want {
		if got[i].ID != want[i].ID || got[i].Body != want[i].Body || got[i].Status != want[i].Status {
			t.Fatalf("block %d diverged:\n got  %+v\n want %+v", i, got[i], want[i])
		}
	}
}
