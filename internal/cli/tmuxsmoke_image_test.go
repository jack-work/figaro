package cli

// ---------------------------------------------------------------------------
// Tool imagery, in a real terminal, against a real model.
//
// Three properties live here that no in-process test can reach:
//
//  1. the picture survives the whole path: read tool, fitter, IR record,
//     encoder, wire, and the MODEL can describe it;
//  2. the note explaining what was done to the picture is rendered where a
//     human can read it;
//  3. not one byte of base64 is painted into the terminal.
//
// (3) is the reason this file exists. The transcript composer takes its own
// path through the IR, and the first time a tool_result carried a megabyte of
// base64 the only thing standing between that and the user's scrollback was a
// switch statement that happened not to have an `image` arm. That is precisely
// the class of bug this suite was written for: invisible to the unit tests,
// immediately obvious to a human, and catastrophic in a pane.
// ---------------------------------------------------------------------------

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"math/rand"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// bigTwoToneImage writes a PNG far too large to inline untouched, whose content
// is unmistakable to a vision model at ANY scale or quality: the left half is
// red, the right half is blue.
//
// It carries per-pixel noise on purpose. A flat image of these dimensions PNGs
// down to a few kilobytes, would fit every budget, and would therefore prove
// nothing about the path under test: the fitter would pass it straight
// through. The noise makes the file honestly large without touching the signal.
func bigTwoToneImage(t *testing.T, dir string) string {
	t.Helper()
	const w, h = 3000, 2000
	rng := rand.New(rand.NewSource(20260729))
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			n := uint8(rng.Intn(40))
			if x < w/2 {
				img.Set(x, y, color.RGBA{R: 200 + n/2, G: 20 + n/4, B: 20 + n/4, A: 255})
			} else {
				img.Set(x, y, color.RGBA{R: 20 + n/4, G: 20 + n/4, B: 200 + n/2, A: 255})
			}
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "twotone.png")
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Logf("fixture %s: %d bytes on disk", path, buf.Len())
	return path
}

// base64ish matches a run long enough that no legitimate UI line could produce
// it. A tool_result's own text is prose and paths; only an inlined image looks
// like this.
var base64ish = regexp.MustCompile(`[A-Za-z0-9+/]{200,}`)

// answerLines counts tok as a whole rendered line, ignoring case and any
// leading gutter the renderer draws.
//
// bodyLines is the same idea but exact-match; this turn's answer is a model's
// free choice of casing, so the comparison is folded. The LINE anchoring is
// what matters and is not relaxed: it is the only thing that separates the
// model's answer from the prompt quoting the sentinel back at us.
func answerLines(capture, tok string) int {
	n := 0
	for _, ln := range strings.Split(capture, "\n") {
		ln = strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(ln), "│>< "))
		if strings.EqualFold(ln, tok) {
			n++
		}
	}
	return n
}

func TestSmoke_ToolImageReachesTheModel(t *testing.T) {
	smokeEnabled(t)
	env, bin := smokeStore(t), smokeBinary(t)
	fixture := bigTwoToneImage(t, t.TempDir())

	// A tall pane: this turn runs a tool and prints a multi-line note, and a
	// short one auto-promotes to the pager where content sits above the tail
	// window and out of the capture.
	p := newPane(t, env, bin, 200, 120)

	p.startTurn("read " + fixture + " and reply with exactly one line of the form " +
		"HALVES=LEFT-RIGHT naming the colour of each half, for example HALVES=GREEN-YELLOW. " +
		"If no image reached you, reply HALVES=BLIND.")
	p.waitIdle(180 * time.Second)

	capture := p.scrollback()

	// Gate every absence on pager chrome before trusting it.
	if n := pagerChrome(capture); n != 0 {
		t.Fatalf("pane promoted to the pager (%d markers); absence assertions would be unsound\n%s", n, capture)
	}

	// (1) The model saw it.
	//
	// TRAP: searching the whole capture is unsound. The prompt is echoed by the
	// shell AND rendered back as the input block, so every sentinel in the
	// instructions: including HALVES=BLIND, appears in the capture before the
	// model has said anything at all. Only a RENDERED BODY LINE is evidence.
	switch {
	case answerLines(capture, "HALVES=BLIND") > 0:
		t.Fatalf("the model reports it received no image: the picture did not survive the path\n%s", capture)
	case answerLines(capture, "HALVES=RED-BLUE") > 0:
		// good
	default:
		t.Errorf("model did not describe the image's halves as red-blue\n%s", capture)
	}

	// (2) The note is rendered. A picture silently altered is the same class of
	// lie as a picture silently dropped: the user must be able to see that the
	// thing the model looked at is not the thing on disk.
	if !strings.Contains(capture, "Multiply coordinates by") {
		t.Errorf("the rescale note never reached the pane; a user cannot tell the image was altered\n%s", capture)
	}

	// (3) No base64 in the terminal.
	if m := base64ish.FindString(capture); m != "" {
		t.Fatalf("base64 leaked into the terminal (%d chars, starts %q)", len(m), m[:60])
	}
}
