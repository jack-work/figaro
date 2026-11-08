package routed

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/store"
)

type stall int

const (
	stallBeforeHeaders stall = iota
	stallAfterHeaders
	keepAliveThenReply
)

func stallingGateway(t *testing.T, how stall, transport string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		if how == stallBeforeHeaders {
			<-r.Context().Done()
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		if how == stallAfterHeaders {
			<-r.Context().Done()
			return
		}
		io.WriteString(w, ": processing\n\n")
		w.(http.Flusher).Flush()
		time.Sleep(150 * time.Millisecond)
		switch transport {
		case "chat":
			io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"late but whole\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
		case "messages":
			io.WriteString(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":1}}}\n\n"+
				"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\"}}\n\n"+
				"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"late but whole\"}}\n\n"+
				"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n"+
				"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":2}}\n\n"+
				"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func sendWithFirstByte(t *testing.T, srv *httptest.Server, transport, timeout string, wrote ...func()) error {
	t.Helper()
	name := "fbgw" + strings.NewReplacer("/", "", "_", "").Replace(t.Name())
	p := gatewayAt(t, name, "preset = \"gateway\"\nbase_url = \""+srv.URL+"/v1\"\n[routing]\ncatalog = \"off\"\ndefault = \""+transport+"\"\n")
	be, aria := store.NewTestAria(t, "d", message.Patch{})
	figLog, _ := be.OpenFigIR(aria)
	_, _ = figLog.Append(store.Entry[message.Message]{Payload: message.Message{
		Role: message.RoleInput, Content: []message.Content{message.TextContent("hi")},
	}})
	p.translators = func(aria, ns string) (store.Log[[]json.RawMessage], error) { return be.OpenTranslator(aria, ns) }
	p.SetModel("explabs/qwen3.8-max")
	raw, _ := json.Marshal(timeout)
	snap := form.FromMap(map[string]json.RawMessage{provider.FirstByteTimeoutKey: raw})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, fn := range wrote {
		ctx = httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{WroteRequest: func(httptrace.WroteRequestInfo) { fn() }})
	}
	return p.Send(ctx, provider.SendInput{AriaID: aria, FigLog: figLog, Snapshot: snap, MaxTokens: 64}, &nullBus{})
}

// qwen3.8-max via rou2 once sent nothing for 90 s and the turn waited until
// it was interrupted. A request with no byte by the deadline is now a cut
// stream, which the turn resends.
func TestARequestThatGetsNoByteByTheDeadlineIsAStreamCut(t *testing.T) {
	for _, transport := range []string{"chat", "messages", "responses"} {
		for name, how := range map[string]stall{"no headers": stallBeforeHeaders, "headers only": stallAfterHeaders} {
			t.Run(transport+"/"+name, func(t *testing.T) {
				var mu sync.Mutex
				var written time.Time
				err := sendWithFirstByte(t, stallingGateway(t, how, transport), transport, "100ms", func() {
					mu.Lock()
					written = time.Now()
					mu.Unlock()
				})
				cut, ok := provider.AsStreamCut(err)
				if !ok {
					t.Fatalf("got %v, want a StreamCut", err)
				}
				if cut.Events != 0 || cut.Awaited != "a first byte" || !strings.HasPrefix(cut.Provider, "fbgw") {
					t.Errorf("cut = %+v", cut)
				}
				if !strings.Contains(err.Error(), "no byte arrived within 100ms; system.first_byte_timeout raises it") {
					t.Errorf("error = %q", err)
				}
				mu.Lock()
				defer mu.Unlock()
				if written.IsZero() {
					t.Fatal("the request was never written, so the clock never ran")
				}
				if waited := time.Since(written); waited > 2*time.Second {
					t.Errorf("waited %s after the request was written for a 100ms deadline", waited)
				}
			})
		}
	}
}

func TestAKeepAliveBeforeTheDeadlineLetsASlowReplyFinish(t *testing.T) {
	for _, transport := range []string{"chat", "messages"} {
		t.Run(transport, func(t *testing.T) {
			if err := sendWithFirstByte(t, stallingGateway(t, keepAliveThenReply, transport), transport, "100ms"); err != nil {
				t.Fatalf("a reply whose first byte came in time failed: %v", err)
			}
		})
	}
}
