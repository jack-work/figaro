package anthropic

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"io"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/store"
)

func noisyPNG(w, h int) string {
	r := rand.New(rand.NewPCG(5, 6))
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

func sendRefusedImage(t *testing.T, refusal string, always bool) ([][]byte, error) {
	t.Helper()
	var (
		mu     sync.Mutex
		bodies [][]byte
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, body)
		n := len(bodies)
		mu.Unlock()
		if n == 1 || always {
			w.WriteHeader(http.StatusBadRequest)
			io.WriteString(w, refusal)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, sseBody(evStart, evText, evDelta("end_turn"), evStop))
	}))
	defer srv.Close()

	be, aria := store.NewTestAria(t, "d", message.Patch{})
	figLog, err := be.OpenFigIR(aria)
	require.NoError(t, err)
	_, err = figLog.Append(store.Entry[message.Message]{Payload: message.Message{Role: message.RoleInput, Content: []message.Content{
		message.ImageContent("image/png", noisyPNG(400, 300)), message.TextContent("noise?"),
	}}})
	require.NoError(t, err)
	rows, err := be.OpenTranslator(aria, "anthropic")
	require.NoError(t, err)
	a, err := New(provider.Knobs{Model: "claude-test", MaxTokens: 64}, nilResolver{},
		func(string) (store.Log[[]json.RawMessage], error) { return rows, nil })
	require.NoError(t, err)
	route := provider.AnthropicDirect()
	route.BaseURL = srv.URL + "/v1"
	a.Route, a.HTTPClient = route, srv.Client()

	err = a.Send(context.Background(), provider.SendInput{AriaID: aria, FigLog: figLog, MaxTokens: 64}, &recordingBus{})
	return bodies, err
}

// Anthropic's refusal of an oversized image, as Copilot's messages route
// passed it through on 2026-11-08, with a limit small enough for the test.
const smallImageRefusal = `{"type":"error","error":{"type":"invalid_request_error","message":"messages.0.content.0.image.source.base64: image exceeds 0.1 MB maximum: 480000 bytes > 100000 bytes"},"request_id":"req_x"}`

func TestTheNativeProviderRefitsARefusedImageAndResendsOnce(t *testing.T) {
	bodies, err := sendRefusedImage(t, smallImageRefusal, false)
	require.NoError(t, err)
	require.Len(t, bodies, 2)
	second := string(bodies[1])
	require.Contains(t, second, "[image downscaled: anthropic refused an image over 97.7KB for model claude-test")
	var req struct {
		Messages []struct {
			Content []struct {
				Type   string `json:"type"`
				Source struct {
					Data string `json:"data"`
				} `json:"source"`
			} `json:"content"`
		} `json:"messages"`
	}
	require.NoError(t, json.Unmarshal(bodies[1], &req))
	for _, m := range req.Messages {
		for _, c := range m.Content {
			if c.Type == "image" {
				require.LessOrEqual(t, len(c.Source.Data), 100000, "the resent image is under the refused limit")
			}
		}
	}
}

func TestTheNativeResendIsNotRepeated(t *testing.T) {
	bodies, err := sendRefusedImage(t, smallImageRefusal, true)
	require.ErrorContains(t, err, "image exceeds")
	require.Len(t, bodies, 2, "one resend, then the refusal stands")
}

func TestOtherNativeRefusalsAreNotResent(t *testing.T) {
	bodies, err := sendRefusedImage(t, `{"type":"error","error":{"type":"invalid_request_error","message":"messages.0: text content blocks must be non-empty"}}`, true)
	require.Error(t, err)
	require.Len(t, bodies, 1)
}
