package figaro

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/png"
	"math/rand/v2"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/tool"
)

func testPNG(t *testing.T, w, h int, noisy bool) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	r := rand.New(rand.NewPCG(1, 2))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			c := color.RGBA{uint8(x), uint8(y), 90, 255}
			if noisy {
				c = color.RGBA{uint8(r.IntN(256)), uint8(r.IntN(256)), uint8(r.IntN(256)), 255}
			}
			img.Set(x, y, c)
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func b64(raw []byte) string { return base64.StdEncoding.EncodeToString(raw) }

func TestPromptImagesKeepsASmallPicture(t *testing.T) {
	raw := testPNG(t, 64, 48, false)
	got, err := promptImages([]rpc.QuaImage{{MimeType: "image/png", Data: b64(raw)}}, "read this", 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Type != message.ContentImage || got[0].MimeType != "image/png" || got[0].ToolCallID != "" {
		t.Fatalf("want one untagged png block, got %+v", got)
	}
	if got[0].Data != b64(raw) {
		t.Fatal("a picture already under every limit must pass through unchanged")
	}
}

// The sender's mime claim is a hint; the bytes decide, so a box cannot
// smuggle a non-image past the check by labelling it.
func TestPromptImagesTrustsTheBytesNotTheLabel(t *testing.T) {
	raw := testPNG(t, 32, 32, false)
	got, err := promptImages([]rpc.QuaImage{{MimeType: "text/plain", Data: b64(raw)}}, "x", 1<<20)
	if err != nil || got[0].MimeType != "image/png" {
		t.Fatalf("a png labelled text/plain: %+v, %v", got, err)
	}
	_, err = promptImages([]rpc.QuaImage{{MimeType: "image/png", Data: b64([]byte("#!/bin/sh\necho hi\n"))}}, "x", 1<<20)
	if err == nil || !strings.Contains(err.Error(), "not png, jpeg, gif or webp") {
		t.Fatalf("a script labelled image/png must be refused, got %v", err)
	}
}

func TestPromptImagesRefusals(t *testing.T) {
	small := rpc.QuaImage{Data: b64(testPNG(t, 16, 16, false))}
	over := rpc.QuaImage{Data: strings.Repeat("A", base64.StdEncoding.EncodedLen(rpc.MaxQuaImageBytes)+4)}
	for _, tc := range []struct {
		name string
		in   []rpc.QuaImage
		text string
		want string
	}{
		{"no text", []rpc.QuaImage{small}, "  ", "give text too"},
		{"too many", []rpc.QuaImage{small, small, small, small, small}, "x", "at most 4"},
		{"oversize", []rpc.QuaImage{over}, "x", "over the 10.0MB limit"},
		{"not base64", []rpc.QuaImage{{Data: "%%%"}}, "x", "not base64"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := promptImages(tc.in, tc.text, 1<<20)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want an error naming %q, got %v", tc.want, err)
			}
		})
	}
}

// The budget is the record's: every picture on one prompt shares it, so the
// user message fits the WAL segment the way a tool round's images do.
func TestPromptImagesShareTheBudget(t *testing.T) {
	raw := testPNG(t, 600, 600, true)
	budget := 200 << 10
	got, err := promptImages([]rpc.QuaImage{{Data: b64(raw)}, {Data: b64(raw)}}, "two", budget)
	if err != nil {
		t.Fatal(err)
	}
	total := 0
	for _, c := range got {
		total += len(c.Data)
	}
	if total > budget {
		t.Fatalf("two images took %d bytes of base64, over the %d budget", total, budget)
	}
	if len(b64(raw)) <= budget/2 {
		t.Fatal("fixture too small to force a refit")
	}
}

func TestMergedPromptsKeepEveryImage(t *testing.T) {
	a := event{typ: eventUserPrompt, id: 1, text: "one", images: []message.Content{message.ImageContent("image/png", "A")}}
	b := event{typ: eventUserPrompt, id: 2, text: "two", images: []message.Content{message.ImageContent("image/png", "B")}}
	got, ok := mergePromptEvents([]event{a, b})
	if !ok || len(got.images) != 2 || got.images[0].Data != "A" || got.images[1].Data != "B" {
		t.Fatalf("a fold dropped an image: %+v", got.images)
	}
}

func BenchmarkPromptImagesPassThrough(b *testing.B) {
	img := image.NewRGBA(image.Rect(0, 0, 1200, 1500))
	for i := range img.Pix {
		img.Pix[i] = uint8(i * 31)
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		b.Fatal(err)
	}
	in := []rpc.QuaImage{{MimeType: "image/png", Data: b64(buf.Bytes())}}
	b.SetBytes(int64(len(in[0].Data)))
	b.ReportAllocs()
	for b.Loop() {
		if _, err := promptImages(in, "total?", tool.ProviderImageCeiling); err != nil {
			b.Fatal(err)
		}
	}
}
