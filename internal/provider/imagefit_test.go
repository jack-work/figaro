package provider

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"math/rand/v2"
	"strings"
	"testing"
)

func noisy(w, h int) string {
	r := rand.New(rand.NewPCG(1, 2))
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.Set(x, y, color.RGBA{uint8(r.IntN(256)), uint8(r.IntN(256)), uint8(r.IntN(256)), 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		panic(err)
	}
	return base64.StdEncoding.EncodeToString(buf.Bytes())
}

func chatPictureRow(t *testing.T, caption, data string) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"role": "user", "content": []any{
		map[string]any{"type": "text", "text": caption},
		map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:image/png;base64," + data}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// Gluck wants a picture sent smaller before it is dropped: over the body
// cap, the oldest images are downscaled first, and only what downscaling
// cannot save is elided.
func TestOverTheBodyCapTheOldestImagesAreDownscaledBeforeAnyIsElided(t *testing.T) {
	big := noisy(400, 300)
	rows := []json.RawMessage{
		chatPictureRow(t, "first", big),
		textRow(t, "between"),
		chatPictureRow(t, "second", big),
	}
	whole, _ := BodySize(chatFrame(SliceRows(rows, nil)))
	limit := whole - len(big)/2

	got, size, err := fit(t, rows, limit)
	if err != nil {
		t.Fatal(err)
	}
	if size > limit {
		t.Fatalf("body %d over the limit %d", size, limit)
	}
	if imagesIn(got) != 2 {
		t.Fatalf("an image was elided where downscaling sufficed: %s", rawStrings(got))
	}
	if !strings.Contains(string(got[0]), "[image downscaled: the request was over rou2's") ||
		!strings.Contains(string(got[0]), "Image: 400x300") {
		t.Errorf("the oldest image does not say it was downscaled, or what it became: %.300s", got[0])
	}
	if string(got[2]) != string(rows[2]) {
		t.Errorf("the newest image was touched while the oldest sufficed")
	}
	if string(got[1]) != string(rows[1]) {
		t.Errorf("text was touched: %s", got[1])
	}
}

func TestEachDialectReadsAndWritesItsImageData(t *testing.T) {
	cases := map[string]struct {
		d    ImageDialect
		part string
	}{
		"messages":  {MessagesImages, `{"type":"image","source":{"type":"base64","media_type":"image/png","data":"AAAA"}}`},
		"chat":      {ChatImages, `{"type":"image_url","image_url":{"url":"data:image/png;base64,AAAA","detail":"high"}}`},
		"responses": {ResponsesImages, `{"type":"input_image","image_url":"data:image/png;base64,AAAA"}`},
	}
	for name, c := range cases {
		var part map[string]any
		if err := json.Unmarshal([]byte(c.part), &part); err != nil {
			t.Fatal(err)
		}
		img, ok := c.d.data(part)
		if !ok || img.Data != "AAAA" || img.Mime != "image/png" {
			t.Errorf("%s: read %+v %v", name, img, ok)
		}
		next := c.d.withData(part, wireImage{Data: "BBBB", Mime: "image/jpeg"})
		again, ok := c.d.data(next)
		if !ok || again.Data != "BBBB" || again.Mime != "image/jpeg" || !c.d.Is(next) {
			t.Errorf("%s: wrote %v", name, next)
		}
		if img, _ := c.d.data(part); img.Data != "AAAA" {
			t.Errorf("%s: writing changed the original part", name)
		}
	}
	var url map[string]any
	_ = json.Unmarshal([]byte(`{"type":"image_url","image_url":{"url":"https://example.com/a.png"}}`), &url)
	if _, ok := ChatImages.data(url); ok {
		t.Error("a remote URL has no data to downscale")
	}
}

func TestADownscaleThatResizesSaysHowToMapCoordinates(t *testing.T) {
	big := noisy(400, 300)
	rows := []json.RawMessage{chatPictureRow(t, "only", big)}
	whole, _ := BodySize(chatFrame(SliceRows(rows, nil)))
	limit := whole - len(big)*9/10

	got, size, err := fit(t, rows, limit)
	if err != nil {
		t.Fatal(err)
	}
	if size > limit || imagesIn(got) != 1 {
		t.Fatalf("size %d (limit %d), %d images", size, limit, imagesIn(got))
	}
	if !strings.Contains(string(got[0]), "Multiply coordinates by") {
		t.Errorf("a resized image must say how to map coordinates: %.300s", got[0])
	}
}

func shapeChat(t *testing.T, rows []json.RawMessage, in SendInput) []json.RawMessage {
	t.Helper()
	got, err := ShapeRequest("rou2", SliceRows(rows, nil), in, 0, ChatImages, chatFrame)
	if err != nil {
		t.Fatal(err)
	}
	out, _ := CollectRows(got)
	return out
}

func TestAnImageCountKeepsTheNewestAndSaysWhy(t *testing.T) {
	small := shot(8, 8)
	rows := []json.RawMessage{chatPictureRow(t, "a", small), textRow(t, "x"), chatPictureRow(t, "b", small), chatPictureRow(t, "c", small)}
	in := SendInput{Images: ImageLimits{MaxCount: 2, CountWhy: "model m takes at most 2 images per request (why)"}}
	got := shapeChat(t, rows, in)
	if imagesIn(got) != 2 || imagesIn(got[:1]) != 0 {
		t.Fatalf("want the two newest kept: %s", rawStrings(got))
	}
	if !strings.Contains(string(got[0]), "[image elided: model m takes at most 2 images per request (why), so figaro dropped the oldest]") {
		t.Errorf("the elided image does not say why: %s", got[0])
	}
	if string(got[1]) != string(rows[1]) || string(got[3]) != string(rows[3]) {
		t.Error("rows that keep their images were rewritten")
	}
}

func TestAnImageOverItsSizeIsDownscaledAndOneThatCannotBeIsElided(t *testing.T) {
	big := noisy(400, 300)
	raw, _ := base64.StdEncoding.DecodeString(big)
	rows := []json.RawMessage{chatPictureRow(t, "big", big), chatPictureRow(t, "garbage", strings.Repeat("A", len(big)))}
	in := SendInput{Images: ImageLimits{MaxBytes: len(raw) / 3, BytesWhy: "model m takes images up to 0.10 MiB (why)"}}
	got := shapeChat(t, rows, in)
	if imagesIn(got[:1]) != 1 || !strings.Contains(string(got[0]), "[image downscaled: model m takes images up to 0.10 MiB (why). Image: 400x300") {
		t.Errorf("the big image was not downscaled with a note: %.300s", got[0])
	}
	img, ok := ChatImages.data(mustPart(t, got[0]))
	if !ok || len(img.Data) > base64.StdEncoding.EncodedLen(len(raw)/3) {
		t.Errorf("the downscaled image is still over the limit")
	}
	if imagesIn(got[1:]) != 0 || !strings.Contains(string(got[1]), "[image elided: model m takes images up to 0.10 MiB (why), and it could not be made that small]") {
		t.Errorf("an image that cannot be decoded must be elided with the reason: %.300s", got[1])
	}
}

func TestNoImageLimitsLeaveTheRequestAlone(t *testing.T) {
	rows := []json.RawMessage{chatPictureRow(t, "big", noisy(64, 64))}
	got := shapeChat(t, rows, SendInput{})
	if string(got[0]) != string(rows[0]) {
		t.Error("a request with no image limits was rewritten")
	}
}

func mustPart(t *testing.T, row json.RawMessage) map[string]any {
	t.Helper()
	var r struct {
		Content []map[string]any `json:"content"`
	}
	if err := json.Unmarshal(row, &r); err != nil {
		t.Fatal(err)
	}
	for _, p := range r.Content {
		if ChatImages.Is(p) {
			return p
		}
	}
	t.Fatalf("no image in %s", row)
	return nil
}
