package routed

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/api/models"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/store"
)

// rou2's answer on 2026-11-08 to dorsal-umpire's turn 53 on
// explabs/glm-5.3-uncensored, whose catalog entry says nothing about images.
const rou2ImagesRefusal = `{"error":{"message":"Message 492 of 729 in this request (role user) carries an image, and the selected model route accepts text only. Images often come from a pasted screenshot, a clipboard paste that included an image, or a tool result earlier in the conversation. Remove the image from the conversation (or start a new session), or choose an image-capable model alias.","type":"invalid_request_error","param":"messages","code":"unsupported_capability"}}`

type refusingGateway struct {
	mu     sync.Mutex
	bodies []string
	refuse func(body string) (int, string)
}

func (g *refusingGateway) serve(t *testing.T) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			io.WriteString(w, `{"data":[{"id":"explabs/glm-5.3-uncensored","name":"GLM-5.3 Uncensored","context_length":1048576,"supports_tools":true}]}`)
			return
		}
		body, _ := io.ReadAll(r.Body)
		g.mu.Lock()
		g.bodies = append(g.bodies, string(body))
		g.mu.Unlock()
		if status, refusal := g.refuse(string(body)); status != 0 {
			w.WriteHeader(status)
			io.WriteString(w, refusal)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"seen\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	t.Cleanup(srv.Close)
	return srv
}

func (g *refusingGateway) sent() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]string(nil), g.bodies...)
}

func sendImageTurn(t *testing.T, p *Provider) error {
	t.Helper()
	be, aria := store.NewTestAria(t, "d", message.Patch{})
	figLog, _ := be.OpenFigIR(aria)
	_, _ = figLog.Append(store.Entry[message.Message]{Payload: message.Message{
		Role: message.RoleInput, Content: []message.Content{
			message.ImageContent("image/png", pngPixel),
			message.TextContent("what is in this picture?"),
		},
	}})
	p.translators = func(aria, ns string) (store.Log[[]json.RawMessage], error) { return be.OpenTranslator(aria, ns) }
	return p.Send(context.Background(), provider.SendInput{AriaID: aria, FigLog: figLog, MaxTokens: 64}, &nullBus{})
}

func refusedGateway(t *testing.T, refuse func(string) (int, string)) (*Provider, *refusingGateway) {
	t.Helper()
	g := &refusingGateway{refuse: refuse}
	srv := g.serve(t)
	name := "refusegw" + strings.ToLower(strings.ReplaceAll(t.Name(), "/", ""))
	p := gatewayAt(t, name, "preset = \"gateway\"\nbase_url = \""+srv.URL+"/v1\"\n")
	p.SetModel("explabs/glm-5.3-uncensored")
	return p, g
}

// Gluck: a model that does not take images gets a stand-in, not an error.
// The refusal swaps every image for the stand-in and sends once more; the
// model is then text-only for the rest of the session, so the next turn goes
// out without images and is not refused again.
func TestAnImageRefusalResendsWithStandInsAndIsRemembered(t *testing.T) {
	p, g := refusedGateway(t, func(body string) (int, string) {
		if strings.Contains(body, `"image_url"`) {
			return http.StatusBadRequest, rou2ImagesRefusal
		}
		return 0, ""
	})
	require.NoError(t, sendImageTurn(t, p))
	sent := g.sent()
	require.Len(t, sent, 2, "the refused request, then one resend")
	require.Contains(t, sent[0], `"image_url"`, "nothing said the model was text-only before the refusal")
	require.NotContains(t, sent[1], `"image_url"`)
	require.Contains(t, sent[1], "takes text only (")
	require.Contains(t, sent[1], "refused its images earlier this session)")

	require.NoError(t, sendImageTurn(t, p))
	require.Len(t, g.sent(), 3, "a later turn is not refused again")
	require.NotContains(t, g.sent()[2], `"image_url"`)

	listed, err := p.Models(context.Background())
	require.NoError(t, err)
	require.Len(t, listed, 1)
	takes, src := listed[0].TakesImages()
	require.False(t, takes)
	require.Equal(t, models.SourceRefusal, src, "the catalog carries the refusal for the session")
}

func TestOnlyTheImageRefusalIsResent(t *testing.T) {
	for name, refusal := range map[string]string{
		"another capability": `{"error":{"message":"the selected model route does not support tools","type":"invalid_request_error","param":"tools","code":"unsupported_capability"}}`,
		"another code":       `{"error":{"message":"Message 3 of 4 carries an image that could not be decoded","type":"invalid_request_error","code":"invalid_image"}}`,
		"not json":           `Message 1 of 2 carries an image, and the selected model route accepts text only`,
	} {
		t.Run(name, func(t *testing.T) {
			p, g := refusedGateway(t, func(string) (int, string) { return http.StatusBadRequest, refusal })
			require.Error(t, sendImageTurn(t, p))
			require.Len(t, g.sent(), 1)
		})
	}
}

// A model that was text-only already (here by the provider file) and is still
// refused is not resent: the stand-ins were in the request.
func TestARefusalOfARequestWithoutImagesIsNotResent(t *testing.T) {
	g := &refusingGateway{refuse: func(string) (int, string) { return http.StatusBadRequest, rou2ImagesRefusal }}
	srv := g.serve(t)
	p := gatewayAt(t, "refusegwtextonly", "preset = \"gateway\"\nbase_url = \""+srv.URL+"/v1\"\n[models.\"explabs/glm-5.3-uncensored\"]\ntext_only = true\n")
	p.SetModel("explabs/glm-5.3-uncensored")
	require.Error(t, sendImageTurn(t, p))
	require.Len(t, g.sent(), 1)
}
