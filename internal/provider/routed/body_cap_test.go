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

type cappedGateway struct {
	mu    sync.Mutex
	sizes []int
	last  string
}

func (g *cappedGateway) serve(t *testing.T, transport string, cap int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		g.mu.Lock()
		g.sizes = append(g.sizes, len(raw))
		g.last = string(raw)
		g.mu.Unlock()
		if len(raw) > cap || !json.Valid(raw) {
			w.WriteHeader(http.StatusBadRequest)
			io.WriteString(w, `{"error":{"message":"Request body must contain valid JSON. Re-encode the payload and resend.","code":"invalid_json"}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		switch transport {
		case "chat":
			io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"seen\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
		case "messages":
			io.WriteString(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":1}}}\n\n"+
				"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\"}}\n\n"+
				"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"seen\"}}\n\n"+
				"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n"+
				"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}\n\n"+
				"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
		case "responses":
			io.WriteString(w, "data: {\"type\":\"response.output_text.delta\",\"output_index\":0,\"content_index\":0,\"item_id\":\"m\",\"delta\":\"seen\"}\n\n"+
				"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"r\",\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"id\":\"m\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"seen\"}]}],\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\n")
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func sendImages(t *testing.T, srv *httptest.Server, transport, file string, images int, imageBytes int) error {
	t.Helper()
	name := "capgw" + strings.NewReplacer("/", "", "_", "").Replace(t.Name())
	p := gatewayAt(t, name, "preset = \"gateway\"\nbase_url = \""+srv.URL+"/v1\"\n"+file+"[routing]\ncatalog = \"off\"\ndefault = \""+transport+"\"\n")
	be, aria := store.NewTestAria(t, "d", message.Patch{})
	figLog, _ := be.OpenFigIR(aria)
	pixels := strings.Repeat("A", imageBytes)
	for i := 0; i < images; i++ {
		_, _ = figLog.Append(store.Entry[message.Message]{Payload: message.Message{
			Role: message.RoleInput, Content: []message.Content{message.ImageContent("image/png", pixels), message.TextContent("look")},
		}})
		_, _ = figLog.Append(store.Entry[message.Message]{Payload: message.Message{
			Role: message.RoleOutput, Content: []message.Content{message.TextContent("seen")}, StopReason: message.StopEnd,
		}})
	}
	_, _ = figLog.Append(store.Entry[message.Message]{Payload: message.Message{
		Role: message.RoleInput, Content: []message.Content{message.ImageContent("image/png", pixels), message.TextContent("and this one?")},
	}})
	p.translators = func(aria, ns string) (store.Log[[]json.RawMessage], error) { return be.OpenTranslator(aria, ns) }
	p.SetModel("explabs/qwen3.8-max")
	return p.Send(context.Background(), provider.SendInput{AriaID: aria, FigLog: figLog, MaxTokens: 64}, &nullBus{})
}

func TestAGatewayWithARequestLimitNeverSendsABodyOverIt(t *testing.T) {
	for _, transport := range []string{"chat", "messages", "responses"} {
		t.Run(transport, func(t *testing.T) {
			const cap = 200_000
			g := &cappedGateway{}
			srv := g.serve(t, transport, cap)
			if err := sendImages(t, srv, transport, "max_request_bytes = 180000\n", 5, 50_000); err != nil {
				t.Fatalf("send: %v", err)
			}
			g.mu.Lock()
			defer g.mu.Unlock()
			if len(g.sizes) != 1 || g.sizes[0] > 180_000 {
				t.Fatalf("bodies %v, want one under 180000", g.sizes)
			}
			if !strings.Contains(g.last, "and this one?") || !strings.Contains(g.last, "image elided") {
				t.Errorf("the newest prompt or the elision note is missing")
			}
			if strings.Count(g.last, strings.Repeat("A", 50_000)) < 1 {
				t.Errorf("the newest image was dropped as well")
			}
		})
	}
}

func TestAGatewayWithoutALimitSendsWhatItHas(t *testing.T) {
	g := &cappedGateway{}
	srv := g.serve(t, "chat", 10_000_000)
	if err := sendImages(t, srv, "chat", "", 5, 50_000); err != nil {
		t.Fatalf("send: %v", err)
	}
	if strings.Contains(g.last, "image elided") || g.sizes[0] < 300_000 {
		t.Errorf("a gateway with no limit had images elided: %d bytes", g.sizes[0])
	}
}
