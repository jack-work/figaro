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

func sendImageThroughChat(t *testing.T, file string, model ...string) []json.RawMessage {
	t.Helper()
	var mu sync.Mutex
	var sent struct {
		Messages []json.RawMessage `json:"messages"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			io.WriteString(w, `{"data":[
				{"id":"explabs/glm-5.3","supported_endpoints":["/chat/completions"],"capabilities":{"image_input":{"supported":false}}},
				{"id":"explabs/qwen3.8-max","supported_endpoints":["/chat/completions"],"capabilities":{"image_input":{"supported":true}}},
				{"id":"copilot-blind","supported_endpoints":["/chat/completions"],"capabilities":{"supports":{"vision":false}}}
			]}`)
			return
		}
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
	if !strings.Contains(file, "[routing]") {
		file += "[routing]\ncatalog = \"off\"\ndefault = \"chat\"\n"
	}
	p := gatewayAt(t, name, "preset = \"gateway\"\nbase_url = \""+srv.URL+"/v1\"\n"+file)

	be, aria := store.NewTestAria(t, "d", message.Patch{})
	figLog, _ := be.OpenFigIR(aria)
	_, _ = figLog.Append(store.Entry[message.Message]{Payload: message.Message{
		Role: message.RoleInput, Content: []message.Content{
			message.ImageContent("image/png", pngPixel),
			message.TextContent("what is in this picture?"),
		},
	}})
	p.translators = func(aria, ns string) (store.Log[[]json.RawMessage], error) { return be.OpenTranslator(aria, ns) }
	m := "explabs/glm-5.3"
	if len(model) > 0 {
		m = model[0]
	}
	p.SetModel(m)
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
	if !strings.Contains(all, "takes text only (text_only in providers/") {
		t.Errorf("the stand-in does not name its reason: %s", all)
	}
}

func joined(msgs []json.RawMessage) string {
	all := ""
	for _, m := range msgs {
		all += string(m)
	}
	return all
}

// gen5 #6: rou2 serves glm-5.3, which takes text only, beside qwen3.8-max,
// which takes images. A provider-wide switch cannot tell them apart.
func TestTextOnlyIsDecidedPerModelInTheProviderFile(t *testing.T) {
	file := "text_only = true\n[routing]\ncatalog = \"off\"\ndefault = \"chat\"\n" +
		"[models.\"explabs/glm-5.3\"]\ntext_only = true\n[models.\"explabs/qwen3.8-max\"]\ntext_only = false\n"
	glm := joined(sendImageThroughChat(t, file, "explabs/glm-5.3"))
	if strings.Contains(glm, "image_url") || !strings.Contains(glm, `model explabs/glm-5.3 takes text only ([models.\"explabs/glm-5.3\"] text_only`) {
		t.Errorf("glm-5.3 got: %s", glm)
	}
	qwen := joined(sendImageThroughChat(t, file, "explabs/qwen3.8-max"))
	if !strings.Contains(qwen, `"type":"image_url"`) || strings.Contains(qwen, "image omitted") {
		t.Errorf("qwen3.8-max, set false over a text-only provider, got: %s", qwen)
	}
}

func TestTextOnlyIsReadFromTheModelCatalog(t *testing.T) {
	file := "[routing]\ncatalog = \"/models\"\n"
	for model, wantImage := range map[string]bool{"explabs/glm-5.3": false, "explabs/qwen3.8-max": true, "copilot-blind": false} {
		got := joined(sendImageThroughChat(t, file, model))
		if has := strings.Contains(got, `"type":"image_url"`); has != wantImage {
			t.Errorf("%s: image sent = %v, want %v: %s", model, has, wantImage, got)
		}
		if !wantImage && !strings.Contains(got, "model catalog") {
			t.Errorf("%s: the stand-in does not name the catalog: %s", model, got)
		}
	}
}
