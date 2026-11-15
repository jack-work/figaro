package routed

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/arialog"
	"github.com/jack-work/figaro/internal/figaro"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/figaro/internal/tool"
	"github.com/stretchr/testify/require"
)

type subscriptionEcho struct{ calls atomic.Int32 }

func (*subscriptionEcho) Name() string        { return "echo" }
func (*subscriptionEcho) Description() string { return "Echo a value" }
func (*subscriptionEcho) Parameters() any {
	return map[string]any{"type": "object", "properties": map[string]any{"value": map[string]any{"type": "string"}}}
}
func (e *subscriptionEcho) Execute(_ context.Context, args map[string]any, _ tool.OnOutput) ([]message.Content, error) {
	e.calls.Add(1)
	return []message.Content{message.TextContent("tool:" + args["value"].(string))}, nil
}

type subscriptionDone struct {
	done chan struct{}
	once sync.Once
}

func (n *subscriptionDone) Notify(method string, _ any) error {
	if method == rpc.MethodTurnDone {
		n.once.Do(func() { close(n.done) })
	}
	return nil
}

func TestDeclaredSubscriptionHTTPToolResultRoundTrip(t *testing.T) {
	var requests atomic.Int32
	var result chan string = make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/codex/responses", r.URL.Path)
		var body map[string]json.RawMessage
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		require.NotContains(t, body, "max_output_tokens")
		require.JSONEq(t, "false", string(body["store"]))
		require.JSONEq(t, "true", string(body["stream"]))
		w.Header().Set("Content-Type", "text/event-stream")
		if requests.Add(1) == 1 {
			io.WriteString(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[{\"type\":\"function_call\",\"call_id\":\"call1\",\"name\":\"echo\",\"arguments\":\"{\\\"value\\\":\\\"ciao\\\"}\"}]}}\n\n")
		} else {
			result <- string(body["input"])
			io.WriteString(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"done\"}]}]}}\n\n")
		}
	}))
	defer server.Close()
	p := gatewayAt(t, "subscription-tool", "preset=\"gateway\"\nbase_url=\""+server.URL+"/codex\"\n[routing]\ncatalog=\"off\"\ndefault=\"responses\"\n[responses]\nomit_fields=[\"max_output_tokens\"]\nno_replay=true\n")
	backend, id := store.NewTestAria(t, "subscription-tool", message.Patch{})
	p.translators = func(id, ns string) (arialog.Log[[]json.RawMessage], error) { return backend.OpenTranslator(id, ns) }
	registry := tool.NewRegistry()
	echo := &subscriptionEcho{}
	require.NoError(t, registry.Register(echo))
	state, err := form.Open("")
	require.NoError(t, err)
	state.Apply(form.Build(form.Snapshot{}, map[string]json.RawMessage{"system.model": json.RawMessage(`"model"`), "system.max_tokens": json.RawMessage(`321`), "system.methods.enabled": json.RawMessage(`false`)}, nil))
	agent := figaro.NewAgent(figaro.Config{ID: id, Provider: p, Backend: backend, Tools: registry, Form: state})
	defer agent.Kill()
	notifier := &subscriptionDone{done: make(chan struct{})}
	unsubscribe := agent.Subscribe(notifier)
	defer unsubscribe()
	agent.SubmitPrompt(rpc.QuaRequest{Text: "Use echo"})
	select {
	case <-notifier.done:
	case <-time.After(5 * time.Second):
		t.Fatal("tool round trip timed out")
	}
	require.Equal(t, int32(2), requests.Load())
	require.Equal(t, int32(1), echo.calls.Load())
	select {
	case replay := <-result:
		require.Contains(t, replay, "function_call_output")
		require.True(t, strings.Contains(replay, "tool:ciao"))
	case <-time.After(time.Second):
		t.Fatal("no tool result request")
	}
}
