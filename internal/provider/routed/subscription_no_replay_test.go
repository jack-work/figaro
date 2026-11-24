package routed

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jack-work/figaro/internal/arialog"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/figaro"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/figaro/internal/tool"
	"github.com/stretchr/testify/require"
)

func TestDeclaredNoReplayStopsPartialSubscriptionAtTheAgentBoundary(t *testing.T) {
	for _, partialTool := range []bool{false, true} {
		t.Run(fmt.Sprint(partialTool), func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				_, _ = io.Copy(io.Discard, r.Body)
				w.Header().Set("Content-Type", "text/event-stream")
				if partialTool {
					io.WriteString(w, "event: response.output_item.added\ndata: {\"type\":\"response.output_item.added\",\"output_index\":0,\"item\":{\"type\":\"function_call\",\"id\":\"item1\",\"call_id\":\"call1\",\"name\":\"echo\",\"arguments\":\"\"}}\n\nevent: response.function_call_arguments.delta\ndata: {\"type\":\"response.function_call_arguments.delta\",\"item_id\":\"item1\",\"delta\":\"{\\\"value\\\":\"}\n\n")
				} else {
					io.WriteString(w, "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"output_index\":0,\"content_index\":0,\"delta\":\"partial answer\"}\n\n")
				}
			}))
			defer server.Close()
			p := gatewayAt(t, "subscription-no-replay-"+fmt.Sprint(partialTool), "preset=\"gateway\"\nbase_url=\""+server.URL+"\"\n[routing]\ncatalog=\"off\"\ndefault=\"responses\"\n[responses]\nno_replay=true\nomit_fields=[\"max_output_tokens\"]\n")
			backend, id := store.NewTestAria(t, "subscription-cut", message.Patch{})
			p.translators = func(id, ns string) (arialog.Log[[]json.RawMessage], error) { return backend.OpenTranslator(id, ns) }
			state, err := form.Open("")
			require.NoError(t, err)
			state.Apply(form.Build(form.Snapshot{}, map[string]json.RawMessage{"system.model": json.RawMessage(`"model"`), "system.methods.enabled": json.RawMessage(`false`)}, nil))
			registry := tool.NewRegistry()
			echo := &subscriptionEcho{}
			require.NoError(t, registry.Register(echo))
			agent := figaro.NewAgent(figaro.Config{ID: id, Provider: p, Backend: backend, Tools: registry, Form: state})
			defer agent.Kill()
			notifier := &subscriptionDone{done: make(chan struct{})}
			unsubscribe := agent.Subscribe(notifier)
			defer unsubscribe()
			agent.SubmitPrompt(rpc.QuaRequest{Text: "Do not retry incomplete output"})
			select {
			case <-notifier.done:
			case <-time.After(5 * time.Minute):
				t.Fatal("partial stream did not finish")
			}
			require.Equal(t, int32(1), requests.Load(), "outer Agent must not create another generation")
			require.Zero(t, echo.calls.Load(), "partial tool must not execute")
			for _, msg := range agent.Context() {
				for _, content := range msg.Content {
					require.False(t, strings.HasPrefix(content.Text, "continue:"))
				}
			}
		})
	}
}
