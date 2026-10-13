package cli

// A tool image reaches the model, its rescale note is rendered, and no base64
// is painted into the terminal.

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

// bigTwoToneImage is red left, blue right, with per-pixel noise: a flat image
// PNGs down to a few kilobytes and the fitter would pass it through untouched.
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

var base64ish = regexp.MustCompile(`[A-Za-z0-9+/]{200,}`)

// answerLines is bodyLines folded for case and stripped of the gutter; the
// whole-line anchoring is what separates the answer from the echoed prompt.
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
	smokeCase(t)
	env, bin := smokeStore(t), smokeBinary(t)
	fixture := bigTwoToneImage(t, t.TempDir())

	// Tall, so the tool and its note do not promote the pager.
	p := newPane(t, env, bin, 200, 120)

	p.startTurn("read " + fixture + " and reply with exactly one line of the form " +
		"HALVES=LEFT-RIGHT naming the colour of each half, for example HALVES=GREEN-YELLOW. " +
		"If no image reached you, reply HALVES=BLIND.")
	p.waitIdle(180 * time.Second)

	capture := p.scrollback()

	if n := pagerChrome(capture); n != 0 {
		t.Fatalf("pane promoted to the pager (%d markers); absence assertions would be unsound\n%s", n, capture)
	}

	// Every sentinel is already in the capture via the echoed prompt; only a
	// whole rendered line is evidence.
	switch {
	case answerLines(capture, "HALVES=BLIND") > 0:
		t.Fatalf("the model reports it received no image: the picture did not survive the path\n%s", capture)
	case answerLines(capture, "HALVES=RED-BLUE") > 0:
	default:
		t.Errorf("model did not describe the image's halves as red-blue\n%s", capture)
	}

	if !strings.Contains(capture, "Multiply coordinates by") {
		t.Errorf("the rescale note never reached the pane; a user cannot tell the image was altered\n%s", capture)
	}

	if m := base64ish.FindString(capture); m != "" {
		t.Fatalf("base64 leaked into the terminal (%d chars, starts %q)", len(m), m[:60])
	}
}
