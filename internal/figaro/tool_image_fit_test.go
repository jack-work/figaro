package figaro

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/png"
	"math/rand"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/internal/config"
)

// bigPNG makes an incompressible PNG of roughly the requested base64 size, so
// a budget test exercises the real encoder rather than a synthetic string.
func bigPNG(t *testing.T, w, h int, seed int64) string {
	t.Helper()
	rng := rand.New(rand.NewSource(seed))
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.Set(x, y, color.RGBA{
				R: uint8(rng.Intn(256)), G: uint8(rng.Intn(256)), B: uint8(rng.Intn(256)), A: 255,
			})
		}
	}
	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, img))
	return base64.StdEncoding.EncodeToString(buf.Bytes())
}

func imagesIn(m message.Message) []message.Content {
	var out []message.Content
	for _, c := range m.Content {
		if c.Type == message.ContentImage {
			out = append(out, c)
		}
	}
	return out
}

func resultTextIn(m message.Message) string {
	var b strings.Builder
	for _, c := range m.Content {
		if c.Type == message.ContentToolResult {
			b.WriteString(c.Text)
		}
	}
	return b.String()
}

// TestAssembleToolResultsRescalesRatherThanDropping is the behaviour this
// change exists for. Several tools returning pictures in one parallel round
// compete for ONE figwal record; the loser used to be discarded, which
// reproduced the exact blindness the images were carried to end. Now the
// budget is met by making the picture smaller, and every image survives.
func TestAssembleToolResultsRescalesRatherThanDropping(t *testing.T) {
	a := &Agent{}
	budget := a.toolImageBudget()

	// Three screenshots that together far exceed one record.
	calls := []message.Content{
		toolCall("tc_1", "shot"),
		toolCall("tc_2", "shot"),
		toolCall("tc_3", "shot"),
	}
	outcomes := map[string]toolOutcome{
		"tc_1": {content: []message.Content{
			message.TextContent("frame one"),
			message.ImageContent("image/png", bigPNG(t, 700, 700, 1)),
		}},
		"tc_2": {content: []message.Content{
			message.TextContent("frame two"),
			message.ImageContent("image/png", bigPNG(t, 700, 700, 2)),
		}},
		"tc_3": {content: []message.Content{
			message.TextContent("frame three"),
			message.ImageContent("image/png", bigPNG(t, 700, 700, 3)),
		}},
	}
	expect := map[string]bool{"tc_1": true, "tc_2": true, "tc_3": true}

	tic := a.assembleToolResults(calls, expect, outcomes)

	imgs := imagesIn(tic)
	require.Len(t, imgs, 3, "no picture may be discarded to make room for another")
	assert.Equal(t, []string{"tc_1", "tc_2", "tc_3"},
		[]string{imgs[0].ToolCallID, imgs[1].ToolCallID, imgs[2].ToolCallID},
		"each surviving image must still name the call that produced it")

	total := 0
	for _, img := range imgs {
		assert.NotEmpty(t, img.Data)
		total += len(img.Data)
	}
	assert.LessOrEqual(t, total, budget, "the record must still fit one WAL segment")

	// Whatever had to give must SAY what gave.
	text := resultTextIn(tic)
	assert.Contains(t, text, "share this message's image budget")
	assert.NotContains(t, text, "image omitted",
		"a fittable image must never be announced as lost")

	// And every survivor must still be a decodable image.
	for _, img := range imgs {
		raw, err := base64.StdEncoding.DecodeString(img.Data)
		require.NoError(t, err)
		_, _, err = image.DecodeConfig(bytes.NewReader(raw))
		require.NoError(t, err, "a rescaled image must still decode")
	}
}

// TestAssembleToolResultsOmissionNoteIsTrue: when an image genuinely cannot be
// carried, the note must say WHY. "Exceeds the budget" is a lie when the
// budget was spent by an earlier call in the same round, and a model reasons
// from what it is told.
func TestAssembleToolResultsOmissionNoteIsTrue(t *testing.T) {
	a := &Agent{}
	budget := a.toolImageBudget()

	calls := []message.Content{toolCall("tc_1", "shot")}
	outcomes := map[string]toolOutcome{
		// Not a decodable image, so the fitter cannot rescue it.
		"tc_1": {content: []message.Content{
			message.ImageContent("image/png", strings.Repeat("A", budget+1)),
		}},
	}

	tic := a.assembleToolResults(calls, map[string]bool{"tc_1": true}, outcomes)

	assert.Empty(t, imagesIn(tic))
	text := resultTextIn(tic)
	assert.Contains(t, text, "image omitted")
	assert.Contains(t, text, "still free in this message")
	assert.NotContains(t, text, "exceeds the",
		"the old wording blamed the total budget even when a sibling had spent it")
}

// TestRefitToolImageReportsAComposableFactor covers the squeeze that quality
// alone cannot absorb. The picture is rescaled rather than dropped, and the
// note gives a FURTHER factor: the read tool already told the model how this
// image relates to the file on disk, and this pass only knows how the new
// pixels relate to the ones it was handed. Two composable factors are correct;
// one absolute-looking factor measured from the wrong baseline is not.
func TestRefitToolImageReportsAComposableFactor(t *testing.T) {
	const limit = 40 << 10
	c := message.ImageContent("image/png", bigPNG(t, 1200, 1200, 4))
	require.Greater(t, len(c.Data), limit)

	fitted, note, ok := refitToolImage(c, limit)
	require.True(t, ok, "a decodable image must be rescued, not discarded")
	assert.LessOrEqual(t, len(fitted.Data), limit)
	assert.True(t, fitted.Resized, "incompressible noise cannot fit on quality alone")
	assert.Contains(t, note, "rescaled")
	assert.Contains(t, note, "FURTHER")
	assert.Contains(t, note, "on top of any factor noted above")
}

// TestRefitToolImageGivesUpQuietlyOnNonImages: the fitter must not turn a
// corrupt payload into a panic or a bogus picture. It reports failure and lets
// the caller write an honest note.
func TestRefitToolImageGivesUpQuietlyOnNonImages(t *testing.T) {
	for _, data := range []string{
		strings.Repeat("A", 100<<10), // not valid base64 (length % 4 != 0)
		base64.StdEncoding.EncodeToString([]byte(strings.Repeat("nope", 30000))),
	} {
		_, _, ok := refitToolImage(message.ImageContent("image/png", data), 32<<10)
		assert.False(t, ok)
	}
}

// loadedWithSegment builds a configuration whose only interesting property is
// its WAL geometry.
func loadedWithSegment(t *testing.T, size int) *config.Loaded {
	t.Helper()
	l := &config.Loaded{}
	l.Config.Store.SegmentSize = &size
	return l
}

// TestAssembleToolResultsBudgetTracksConfig pins the derivation the whole
// design rests on: the turn loop must spend the store's number, so raising
// store.segment_size raises what a screenshot may cost.
func TestAssembleToolResultsBudgetTracksConfig(t *testing.T) {
	small := &Agent{settings: loadedWithSegment(t, 1<<20)}
	large := &Agent{settings: loadedWithSegment(t, 8<<20)}

	assert.Less(t, small.toolImageBudget(), large.toolImageBudget(),
		"a bigger segment must buy a bigger picture")
	assert.Less(t, small.toolImageBudget(), 1<<20,
		"imagery must not claim the whole record")
}
