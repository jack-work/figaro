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

	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/store"
)

const pngPixel = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg=="

func sendImageThroughChat(t *testing.T, file string) []json.RawMessage {
	t.Helper()
	var mu sync.Mutex
	var sent struct {
		Messages []json.RawMessage `json:"messages"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			http.NotFound(w, r)
			return
		}
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		_ = json.Unmarshal(body, &sent)
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"a red pixel\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer srv.Close()
	name := "imggw" + strings.ReplaceAll(t.Name(), "/", "")
	p := gatewayAt(t, name, "preset = \"gateway\"\nbase_url = \""+srv.URL+"/v1\"\n"+file+"[routing]\ncatalog = \"off\"\ndefault = \"chat\"\n")

	be, aria := store.NewTestAria(t, "d", message.Patch{})
	figLog, _ := be.OpenFigIR(aria)
	_, _ = figLog.Append(store.Entry[message.Message]{Payload: message.Message{
		Role: message.RoleInput, Content: []message.Content{
			message.ImageContent("image/png", pngPixel),
			message.TextContent("what is in this picture?"),
		},
	}})
	p.translators = func(aria, ns string) (store.Log[[]json.RawMessage], error) { return be.OpenTranslator(aria, ns) }
	p.SetModel("explabs/glm-5.3")
	if err := p.Send(context.Background(), provider.SendInput{AriaID: aria, FigLog: figLog, MaxTokens: 64}, &nullBus{}); err != nil {
		t.Fatalf("send: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	return sent.Messages
}

// rou2's explabs models on the chat wire answered "no image reached me":
// the image was swapped for a stand-in because the route sends bare-string
// content for caching, which says nothing about whether it takes images.
func TestAChatRouteWithBareStringContentStillSendsTheImage(t *testing.T) {
	msgs := sendImageThroughChat(t, "")
	all := ""
	for _, m := range msgs {
		all += string(m)
	}
	if !strings.Contains(all, `"type":"image_url"`) || !strings.Contains(all, "data:image/png;base64,"+pngPixel) {
		t.Fatalf("no image_url part reached the endpoint: %s", all)
	}
	if strings.Contains(all, "image omitted") {
		t.Errorf("the stand-in was sent beside the image: %s", all)
	}
	if !strings.Contains(all, "what is in this picture?") {
		t.Errorf("the question was lost: %s", all)
	}
}

func TestATextOnlyChatRouteSaysWhyTheImageWasLeftOut(t *testing.T) {
	msgs := sendImageThroughChat(t, "text_only = true\n")
	all := ""
	for _, m := range msgs {
		all += string(m)
	}
	if strings.Contains(all, "image_url") {
		t.Fatalf("a text-only route was sent an image part: %s", all)
	}
	if !strings.Contains(all, "declares text_only = true") {
		t.Errorf("the stand-in does not name its reason: %s", all)
	}
}
