package provider

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/internal/tool"
)

func shot(w, h int) string {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.Draw(img, img.Bounds(), &image.Uniform{color.RGBA{40, 90, 200, 255}}, image.Point{}, draw.Src)
	var buf bytes.Buffer
	png.Encode(&buf, img)
	return base64.StdEncoding.EncodeToString(buf.Bytes())
}

// broken is an image whose header reads fine and whose pixels do not: the one
// case where a picture is recognisable, unsendable and unfixable.
func broken(w, h int) string {
	raw, _ := base64.StdEncoding.DecodeString(shot(w, h))
	return base64.StdEncoding.EncodeToString(raw[:len(raw)/2])
}

// A RECORD WRITTEN BEFORE THE CEILING EXISTED IS STILL REPAIRED, which is why
// this runs on the way to the encoder rather than on the way to the log.
// Thirteen such images were in the store when this was written, and each one
// ended every turn of the aria holding it.
func TestSendableImagesShrinksAnOversizedRecord(t *testing.T) {
	original := shot(2800, 640)
	msg := message.Message{Role: message.RoleInput, Content: []message.Content{
		message.TextContent("before"),
		message.ImageContent("image/png", original),
		message.TextContent("after"),
	}}
	out, changed := SendableImages(msg)
	if !changed {
		t.Fatal("a 2800px image needs changing")
	}
	raw, _ := base64.StdEncoding.DecodeString(out.Content[1].Data)
	cfg, _, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Width > tool.DefaultMaxSendDim || cfg.Height > tool.DefaultMaxSendDim {
		t.Errorf("sent %dx%d, over the ceiling", cfg.Width, cfg.Height)
	}
	if !strings.Contains(out.Content[1].Text, "coordinates") {
		t.Errorf("a resize must tell the model its coordinates moved: %q", out.Content[1].Text)
	}
	if len(out.Content) != 3 || out.Content[0].Text != "before" || out.Content[2].Text != "after" {
		t.Error("the surrounding blocks must not move")
	}
	if msg.Content[1].Data != original {
		t.Error("the log's copy is shared; rewriting it changes what every other reader sees")
	}
}

// An image inside the ceiling is passed through untouched: the common case must
// not pay for the rare one.
func TestSendableImagesLeavesAcceptableImagesAlone(t *testing.T) {
	fine := shot(900, 400)
	msg := message.Message{Role: message.RoleInput, Content: []message.Content{
		message.ImageContent("image/png", fine),
	}}
	if out, changed := SendableImages(msg); changed || out.Content[0].Data != fine {
		t.Error("a 900px image needs no change and must not be re-encoded")
	}
	// Nor does one this package cannot read at all: Go decodes png, jpeg, gif
	// and webp, and a provider may take something it does not.
	msg = message.Message{Role: message.RoleInput, Content: []message.Content{
		message.ImageContent("image/heic", "bm90IGFuIGltYWdl"),
	}}
	if _, changed := SendableImages(msg); changed {
		t.Error("an unreadable payload is not something this can judge")
	}
}

// AN IMAGE THAT CANNOT BE SENT BECOMES WORDS, IN PLACE: the model is told what
// happened instead of the turn being killed, and no provenance index moves.
func TestSendableImagesReplacesTheUnfittableWithText(t *testing.T) {
	msg := message.Message{Role: message.RoleInput, Content: []message.Content{
		message.TextContent("head"),
		message.ImageContent("image/png", broken(2800, 640)),
		message.TextContent("tail"),
	}}
	out, changed := SendableImages(msg)
	if !changed {
		t.Fatal("an unsendable image must be dealt with")
	}
	if out.Content[1].Type != message.ContentProse || !strings.Contains(out.Content[1].Text, "image omitted") {
		t.Errorf("want prose saying why, got %q / %q", out.Content[1].Type, out.Content[1].Text)
	}
	if len(out.Content) != 3 || out.Content[2].Text != "tail" {
		t.Error("substitution must be one for one")
	}
}
