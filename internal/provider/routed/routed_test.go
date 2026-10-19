package routed

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/BurntSushi/toml"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/store"
)

// Shapes from Copilot's live catalog.
func TestTransportForEndpoints(t *testing.T) {
	cases := map[string]struct {
		endpoints []string
		want      Transport
	}{
		"claude":       {[]string{"/v1/messages", "/chat/completions"}, Messages},
		"gemini":       {[]string{"/chat/completions"}, Chat},
		"gpt-5.4":      {[]string{"/responses", "/chat/completions", "ws:/responses"}, ResponsesWS},
		"socket first": {[]string{"ws:/responses", "/responses"}, ResponsesWS},
		"grok":         {[]string{"/responses"}, Responses},
		"embedding":    {nil, ""},
	}
	for name, tc := range cases {
		if got := transportForEndpoints(tc.endpoints); got != tc.want {
			t.Errorf("%s %v: got %q, want %q", name, tc.endpoints, got, tc.want)
		}
	}
}

func TestRouteOrderIsRuleThenCatalogThenDefault(t *testing.T) {
	p := mustNew(t, Spec{
		Name:    "t",
		BaseURL: func() string { return "http://127.0.0.1:1" },
		Catalog: "/models",
		Default: Chat,
		Rules:   []Rule{{Model: "grok-*", Transport: Responses}},
	})
	p.catalog = map[string]catalogModel{
		"grok-4.7": {ID: "grok-4.7", SupportedEndpoints: []string{"/v1/messages"}},
		"claude-x": {ID: "claude-x", SupportedEndpoints: []string{"/v1/messages"}},
		"silent":   {ID: "silent"},
	}
	for model, want := range map[string]Transport{"grok-4.7": Responses, "claude-x": Messages, "silent": Chat} {
		got, err := p.route(context.Background(), model)
		if err != nil || got != want {
			t.Errorf("%s: got %q, %v; want %q", model, got, err, want)
		}
	}

	p.spec.Default = ""
	if _, err := p.route(context.Background(), "silent"); err == nil || !strings.Contains(err.Error(), "no route") {
		t.Errorf("a model with no rule, no catalog transport and no default must be refused, got %v", err)
	}
}

func TestEachTransportKeepsItsTranslatorChannel(t *testing.T) {
	copilot := Spec{Name: "copilot"}
	gateway := Spec{Name: "gateway"}
	for got, want := range map[string]string{
		copilot.namespace(Messages):    "copilot-messages",
		copilot.namespace(Responses):   "copilot-responses",
		copilot.namespace(ResponsesWS): "copilot-responses",
		gateway.namespace(Chat):        "gateway",
	} {
		if got != want {
			t.Errorf("channel %q, want %q: a renamed channel orphans every existing aria's translation cache", got, want)
		}
	}
}

func TestOnlyStaticRoutingTranslatesOnAppend(t *testing.T) {
	base := func() string { return "http://127.0.0.1:1" }
	static := mustNew(t, Spec{Name: "gw", BaseURL: base, Default: Chat})
	if got := static.TranslatorChannel(); got != "gw" {
		t.Errorf("static chat routing: channel %q, want gw", got)
	}
	routed := mustNew(t, Spec{Name: "gw", BaseURL: base, Default: Chat, Rules: []Rule{{Model: "grok-*", Transport: Responses}}})
	if got := routed.TranslatorChannel(); got != "" {
		t.Errorf("per-model routing: channel %q, want none", got)
	}
}

// The catalog form records what the gateway enforces: the per-tier prompt cap,
// not the model's raw window, and a picker-hidden model is kept (flagged) so an
// aria already pinned to it still has a capacity.
func TestModelsReportPromptCapsAndKeepHiddenModels(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			http.NotFound(w, r)
			return
		}
		_, _ = io.WriteString(w, `{"data":[
			{"id":"m","name":"Em","supported_endpoints":["/responses"],
			 "billing":{"token_prices":{"default":{"max_prompt_tokens":128000},"long_context":{"max_prompt_tokens":1000000}}},
			 "capabilities":{"limits":{"max_context_window_tokens":1000000,"max_output_tokens":64000}}},
			{"id":"hidden","model_picker_enabled":false,"supported_endpoints":["/chat/completions"],
			 "capabilities":{"limits":{"max_context_window_tokens":200000}}}
		]}`)
	}))
	defer srv.Close()
	p := mustNew(t, Spec{Name: "gw", BaseURL: func() string { return srv.URL }, Catalog: "/models"})

	got, err := p.Models(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]provider.ModelInfo{}
	for _, m := range got {
		byID[m.ID] = m
	}
	m := byID["m"]
	if m.ContextWindow != 128000 || m.LongContextWindow != 1000000 || m.MaxTokens != 64000 || m.Hidden {
		t.Errorf("m: %+v", m)
	}
	h, ok := byID["hidden"]
	if !ok || !h.Hidden || h.ContextWindow != 200000 {
		t.Errorf("hidden: %+v (present %v)", h, ok)
	}
	if picks := provider.Pickable(got); len(picks) != 1 || picks[0].ID != "m" {
		t.Errorf("the picker must not offer a hidden model: %+v", picks)
	}
}

type refusingResolver struct{ resolves int }

func (r *refusingResolver) Resolve() (string, error) {
	r.resolves++
	return "", errors.New("credentials must not be resolved")
}
func (r *refusingResolver) Invalidate(string) error { return nil }

func TestKnownModelSettingsAreRefusedBeforeAnyNetwork(t *testing.T) {
	src := &refusingResolver{}
	p, err := New(Spec{Name: "t", BaseURL: func() string { return "http://127.0.0.1:1" }, Catalog: "/models"},
		provider.Knobs{Model: "gpt-6-astra"}, src, nil)
	if err != nil {
		t.Fatal(err)
	}
	err = p.Send(context.Background(), provider.SendInput{
		Snapshot: form.FromMap(map[string]json.RawMessage{"system.thinking_effort": json.RawMessage(`"none"`)}),
	}, &captureBus{})
	if err == nil || !strings.Contains(err.Error(), "system.thinking_effort") {
		t.Fatalf("got %v, want the setting named", err)
	}
	if src.resolves != 0 {
		t.Errorf("resolved credentials %d times before local validation", src.resolves)
	}
}

func TestATOMLFileBecomesASpec(t *testing.T) {
	var f File
	if _, err := toml.Decode(`
base_url = "http://gw.local/"
headers = { "X-Team" = "figaro" }
[transport_headers.responses]
"X-Only-Responses" = "1"
[routing]
catalog = "models"
default = "chat"
[[routing.rule]]
model = "grok-*"
transport = "responses"
`, &f); err != nil {
		t.Fatal(err)
	}
	spec, err := f.Apply(Spec{Name: "gateway", Rules: []Rule{{Model: "preset-*", Transport: Messages}}})
	if err != nil {
		t.Fatal(err)
	}
	if spec.BaseURL() != "http://gw.local" || spec.Catalog != "/models" || spec.Default != Chat {
		t.Errorf("base %q catalog %q default %q", spec.BaseURL(), spec.Catalog, spec.Default)
	}
	if len(spec.Rules) != 2 || spec.Rules[0].Model != "grok-*" || spec.Rules[1].Model != "preset-*" {
		t.Errorf("rules %+v: the file's rules must come before the preset's", spec.Rules)
	}
	if h := spec.headersFor(Responses); h["X-Team"] != "figaro" || h["X-Only-Responses"] != "1" {
		t.Errorf("responses headers %v", h)
	}
	if h := spec.headersFor(Chat); h["X-Only-Responses"] != "" {
		t.Errorf("a transport header leaked onto chat: %v", h)
	}
}

func TestATOMLFileIsRefusedRatherThanGuessed(t *testing.T) {
	cases := map[string]string{
		"unknown transport":  "[routing]\ndefault = \"grpc\"\n",
		"rule with no route": "[routing]\ndefault = \"chat\"\n[[routing.rule]]\nmodel = \"x\"\n",
		"bad glob":           "[routing]\ndefault = \"chat\"\n[[routing.rule]]\nmodel = \"[\"\ntransport = \"chat\"\n",
		"bad header table":   "[transport_headers.carrier-pigeon]\nX = \"1\"\n[routing]\ndefault = \"chat\"\n",
		"nowhere to go":      "base_url = \"http://x\"\n",
	}
	for name, doc := range cases {
		var f File
		if _, err := toml.Decode(doc, &f); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if _, err := f.Apply(Spec{Name: "gateway", BaseURL: func() string { return "http://x" }}); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

const chatSSE = "data: {\"choices\":[{\"delta\":{\"content\":\"chat\"}}]}\n\n" +
	"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":1}}\n\n" +
	"data: [DONE]\n\n"

const responsesSSE = "event: response.output_text.delta\n" +
	"data: {\"type\":\"response.output_text.delta\",\"output_index\":0,\"content_index\":0,\"delta\":\"responses\"}\n\n" +
	"event: response.completed\n" +
	"data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"responses\"}]}],\"usage\":{\"input_tokens\":3,\"output_tokens\":1}}}\n\n"

type hit struct {
	path    string
	headers http.Header
	model   string
}

// One gateway, two models, two dialects.
func TestAGatewayRoutesEachModelToTheTransportItsFileNames(t *testing.T) {
	var mu sync.Mutex
	var hits []hit
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body struct {
			Model string `json:"model"`
		}
		_ = json.Unmarshal(raw, &body)
		mu.Lock()
		hits = append(hits, hit{r.URL.Path, r.Header.Clone(), body.Model})
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		switch r.URL.Path {
		case "/v1/chat/completions":
			_, _ = io.WriteString(w, chatSSE)
		case "/v1/responses":
			_, _ = io.WriteString(w, responsesSSE)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	var f File
	doc := fmt.Sprintf(`
base_url = "%s/v1"
headers = { "X-Team" = "figaro" }
[transport_headers.responses]
"X-Only-Responses" = "1"
[routing]
default = "chat"
[[routing.rule]]
model = "grok-*"
transport = "responses"
`, srv.URL)
	if _, err := toml.Decode(doc, &f); err != nil {
		t.Fatal(err)
	}
	spec, err := f.Apply(Spec{Name: "gateway", AuthOptional: true})
	if err != nil {
		t.Fatal(err)
	}

	for model, want := range map[string]string{"llama-4": "chat", "grok-4.7": "responses"} {
		be, aria := store.NewTestAria(t, "d", message.Patch{})
		figLog, err := be.OpenFigIR(aria)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := figLog.Append(store.Entry[message.Message]{Payload: message.Message{
			Role: message.RoleInput, Content: []message.Content{message.TextContent("say something")},
		}}); err != nil {
			t.Fatal(err)
		}
		p, err := New(spec, provider.Knobs{Model: model, MaxTokens: 64}, staticToken("k"),
			func(aria, ns string) (store.Log[[]json.RawMessage], error) { return be.OpenTranslator(aria, ns) })
		if err != nil {
			t.Fatal(err)
		}
		bus := &captureBus{}
		if err := p.Send(context.Background(), provider.SendInput{AriaID: aria, FigLog: figLog, MaxTokens: 64}, bus); err != nil {
			t.Fatalf("%s: %v", model, err)
		}
		if got := bus.text.String(); got != want {
			t.Errorf("%s streamed %q, want the %s reply", model, got, want)
		}
	}

	mu.Lock()
	defer mu.Unlock()
	if len(hits) != 2 {
		t.Fatalf("%d requests, want 2: %+v", len(hits), hits)
	}
	for _, h := range hits {
		if h.headers.Get("X-Team") != "figaro" {
			t.Errorf("%s: the file's headers were not sent", h.path)
		}
		if h.headers.Get("Authorization") != "Bearer k" {
			t.Errorf("%s: Authorization %q", h.path, h.headers.Get("Authorization"))
		}
		only := h.headers.Get("X-Only-Responses")
		if (h.path == "/v1/responses") != (only == "1") {
			t.Errorf("%s: X-Only-Responses=%q", h.path, only)
		}
	}
}

func mustNew(t *testing.T, spec Spec) *Provider {
	t.Helper()
	p, err := New(spec, provider.Knobs{Model: "m"}, staticToken("k"), nil)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

type captureBus struct{ text strings.Builder }

func (b *captureBus) PushDelta(c message.Content)                            { b.text.WriteString(c.Text) }
func (b *captureBus) PushToolInvokeStart(string, string)                     {}
func (b *captureBus) PushToolInvokeDelta(string, string)                     {}
func (b *captureBus) PushToolReady(message.Content)                          {}
func (b *captureBus) PushMessageEnd(string)                                  {}
func (b *captureBus) PushFigaro(message.Message, ...provider.AssistantCache) {}

// Rows as the pre-routing binary wrote them: a different string here
// re-encodes every existing Responses aria's history.
func TestCopilotResponsesFingerprintIsUnchanged(t *testing.T) {
	p := mustNew(t, Spec{Name: "copilot", BaseURL: func() string { return "" }, Catalog: "/models"})
	p.SetModel("gpt-6-astra")
	if got := p.responsesBackend().Fingerprint(); got != "copilot-responses/v5/gpt-6-astra" {
		t.Errorf("fingerprint %q", got)
	}
}

const messagesSSE = "event: message_start\n" +
	`data: {"type":"message_start","message":{"usage":{"input_tokens":5}}}` + "\n\n" +
	"event: content_block_start\n" +
	`data: {"type":"content_block_start","index":0,"content_block":{"type":"text"}}` + "\n\n" +
	"event: content_block_delta\n" +
	`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"messages"}}` + "\n\n" +
	"event: content_block_stop\n" +
	`data: {"type":"content_block_stop","index":0}` + "\n\n" +
	"event: message_delta\n" +
	`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}` + "\n\n" +
	"event: message_stop\n" +
	`data: {"type":"message_stop"}` + "\n\n"

type revocable struct {
	mu      sync.Mutex
	current string
	minted  int
}

func (r *revocable) Resolve() (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.current == "" {
		r.minted++
		r.current = fmt.Sprintf("tok-%d", r.minted)
	}
	return r.current, nil
}

func (r *revocable) Invalidate(token string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.current == token {
		r.current = ""
	}
	return nil
}

// A gateway that revokes a service token must get a fresh one on the next
// request, on every transport, rather than a 401 until the token expires.
func TestAGatewayRejectionRenewsTheTokenOnEveryTransport(t *testing.T) {
	var mu sync.Mutex
	revoked := map[string]bool{}
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		mu.Lock()
		seen = append(seen, r.URL.Path+" "+tok)
		bad := revoked[tok]
		mu.Unlock()
		_, _ = io.Copy(io.Discard, r.Body)
		if bad {
			http.Error(w, "revoked", http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/v1/chat/completions":
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, chatSSE)
		case "/v1/messages":
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, messagesSSE)
		case "/v1/models":
			_, _ = io.WriteString(w, `{"data":[{"id":"llama-4"}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	var f File
	doc := fmt.Sprintf("base_url = %q\nmessages_path = \"/messages\"\n[routing]\ndefault = \"chat\"\ncatalog = \"/models\"\n[[routing.rule]]\nmodel = \"claude-*\"\ntransport = \"messages\"\n", srv.URL+"/v1")
	if _, err := toml.Decode(doc, &f); err != nil {
		t.Fatal(err)
	}
	spec, err := f.Apply(Spec{Name: "rou2"})
	if err != nil {
		t.Fatal(err)
	}
	tokens := &revocable{}
	revoke := func() {
		cur, _ := tokens.Resolve()
		mu.Lock()
		revoked[cur] = true
		mu.Unlock()
	}

	for _, model := range []string{"llama-4", "claude-sonnet-5.5"} {
		be, aria := store.NewTestAria(t, "d", message.Patch{})
		figLog, err := be.OpenFigIR(aria)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := figLog.Append(store.Entry[message.Message]{Payload: message.Message{
			Role: message.RoleInput, Content: []message.Content{message.TextContent("say something")},
		}}); err != nil {
			t.Fatal(err)
		}
		p, err := New(spec, provider.Knobs{Model: model, MaxTokens: 64}, tokens,
			func(aria, ns string) (store.Log[[]json.RawMessage], error) { return be.OpenTranslator(aria, ns) })
		if err != nil {
			t.Fatal(err)
		}
		revoke()
		if _, err := p.fetchCatalog(context.Background()); err != nil {
			t.Fatalf("%s catalog: %v", model, err)
		}
		revoke()
		if err := p.Send(context.Background(), provider.SendInput{AriaID: aria, FigLog: figLog, MaxTokens: 64}, &captureBus{}); err != nil {
			t.Fatalf("%s: %v", model, err)
		}
	}

	mu.Lock()
	defer mu.Unlock()
	want := []string{
		"/v1/models tok-1", "/v1/models tok-2", "/v1/chat/completions tok-2", "/v1/chat/completions tok-3",
		"/v1/models tok-3", "/v1/models tok-4", "/v1/messages tok-4", "/v1/messages tok-5",
	}
	if strings.Join(seen, "\n") != strings.Join(want, "\n") {
		t.Fatalf("requests:\n%s\nwant:\n%s", strings.Join(seen, "\n"), strings.Join(want, "\n"))
	}
}
