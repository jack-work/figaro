package copilot

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/store"
)

func noisyPNG(w, h int) string {
	r := rand.New(rand.NewPCG(3, 4))
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.Set(x, y, color.RGBA{uint8(r.IntN(256)), uint8(r.IntN(256)), uint8(r.IntN(256)), 255})
		}
	}
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return base64.StdEncoding.EncodeToString(buf.Bytes())
}

// The refusal Copilot's messages route returned on 2026-11-08 for a
// 23,068,737-byte PNG sent to claude-opus-5.5 (Anthropic's own wording,
// counting base64 bytes).
const copilotImageRefusal = `{"type":"error","error":{"type":"invalid_request_error","message":"messages.0.content.0.image.source.base64: image exceeds 10 MB maximum: 30758316 bytes > 10485760 bytes"},"request_id":"req_011CgrDcYyzs5GRdvHGMSqqs"}`

// Copilot's catalog numbers are not enforced up front (it takes more than it
// states), but once it refuses an image the request is refitted to them and
// sent once more.
func TestAnImageRefusalRefitsToTheCatalogAndResendsOnce(t *testing.T) {
	big := noisyPNG(400, 300)
	var (
		mu     sync.Mutex
		bodies [][]byte
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/models" {
			io.WriteString(w, `{"data":[{"id":"claude-test","supported_endpoints":["/v1/messages"],
				"capabilities":{"limits":{"vision":{"max_prompt_image_size":100000,"max_prompt_images":1}},"supports":{"vision":true}}}]}`)
			return
		}
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, body)
		n := len(bodies)
		mu.Unlock()
		if n == 1 {
			w.WriteHeader(http.StatusBadRequest)
			io.WriteString(w, copilotImageRefusal)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		for _, ev := range []string{
			"event: message_start\n" + `data: {"type":"message_start","message":{"id":"m","role":"assistant","model":"claude-test","content":[],"usage":{"input_tokens":3,"output_tokens":0}}}`,
			"event: content_block_start\n" + `data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			"event: content_block_delta\n" + `data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"noise"}}`,
			"event: content_block_stop\n" + `data: {"type":"content_block_stop","index":0}`,
			"event: message_delta\n" + `data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}`,
			"event: message_stop\n" + `data: {"type":"message_stop"}`,
		} {
			fmt.Fprint(w, ev+"\n\n")
		}
	}))
	defer srv.Close()

	be, aria := store.NewTestAria(t, "d", message.Patch{})
	figLog, _ := be.OpenFigIR(aria)
	_, _ = figLog.Append(store.Entry[message.Message]{Payload: message.Message{Role: message.RoleInput, Content: []message.Content{
		message.ImageContent("image/png", big), message.ImageContent("image/png", noisyPNG(16, 16)), message.TextContent("noise?"),
	}}})
	c, err := newTestCopilot(srv.URL, "claude-test", staticResolver("gh-token"),
		func(aria, ns string) (store.Log[[]json.RawMessage], error) { return be.OpenTranslator(aria, ns) })
	if err != nil {
		t.Fatal(err)
	}
	bus := &captureBus{}
	if err := c.Send(context.Background(), provider.SendInput{AriaID: aria, FigLog: figLog, MaxTokens: 64}, bus); err != nil {
		t.Fatalf("send: %v", err)
	}
	if len(bodies) != 2 {
		t.Fatalf("%d requests, want the refused one and one resend", len(bodies))
	}
	if !strings.Contains(string(bodies[0]), big) {
		t.Fatal("the first request was fitted up front to Copilot's own numbers")
	}
	second := string(bodies[1])
	if strings.Contains(second, big) || strings.Count(second, `"type":"image"`) != 2 {
		t.Errorf("the resend must carry both images, the big one smaller: a size refusal does not apply the catalog's count")
	}
	if !strings.Contains(second, "[image downscaled: model claude-test takes images up to 97.7KB (copilot's model catalog)") {
		t.Errorf("the resend does not say why the image is smaller: %.400s", second)
	}
}

func TestOtherRefusalsAreNotRefitted(t *testing.T) {
	var n int
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/models" {
			io.WriteString(w, `{"data":[]}`)
			return
		}
		mu.Lock()
		n++
		mu.Unlock()
		w.WriteHeader(http.StatusBadRequest)
		io.WriteString(w, `{"type":"error","error":{"type":"invalid_request_error","message":"messages.0: text content blocks must be non-empty"}}`)
	}))
	defer srv.Close()
	be, aria := store.NewTestAria(t, "d", message.Patch{})
	figLog, _ := be.OpenFigIR(aria)
	_, _ = figLog.Append(store.Entry[message.Message]{Payload: message.Message{Role: message.RoleInput, Content: []message.Content{
		message.ImageContent("image/png", noisyPNG(32, 32)), message.TextContent("hi"),
	}}})
	c, _ := newTestCopilot(srv.URL, "claude-test", staticResolver("gh-token"),
		func(aria, ns string) (store.Log[[]json.RawMessage], error) { return be.OpenTranslator(aria, ns) })
	if err := c.Send(context.Background(), provider.SendInput{AriaID: aria, FigLog: figLog, MaxTokens: 64}, &captureBus{}); err == nil {
		t.Fatal("a refused request succeeded")
	}
	if n != 1 {
		t.Errorf("%d requests for a refusal that is not about images, want 1", n)
	}
}
