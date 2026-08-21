package openaichat

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/internal/provider"
)

func snap(t *testing.T, kv map[string]any) form.Snapshot {
	t.Helper()
	m := map[string]json.RawMessage{}
	for k, v := range kv {
		raw, err := json.Marshal(v)
		if err != nil {
			t.Fatalf("marshal %s: %v", k, err)
		}
		m[k] = raw
	}
	return form.FromMap(m)
}

func newTestProvider(t *testing.T, route provider.Route, mode provider.MarkMode) *Provider {
	t.Helper()
	p, err := New(provider.Knobs{Model: "claude-sonnet-4-6", MaxTokens: 1024}, staticToken("k"), route, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	p.setMarkMode(mode)
	return p
}

func encodeAll(t *testing.T, p *Provider, msgs []message.Message) [][]json.RawMessage {
	t.Helper()
	var out [][]json.RawMessage
	for _, m := range msgs {
		raw, err := p.encode(m, form.Snapshot{})
		if err != nil {
			t.Fatalf("encode: %v", err)
		}
		if len(raw) > 0 {
			out = append(out, raw)
		}
	}
	return out
}

func conversation() []message.Message {
	return []message.Message{
		{Role: message.RoleInput, Content: []message.Content{message.TextContent("first turn")}},
		{Role: message.RoleOutput, Content: []message.Content{message.TextContent("first reply")}},
		{Role: message.RoleInput, Content: []message.Content{message.TextContent("second turn")}},
	}
}

// --- king's assertion (d): never more than four markers, in any mode -------

func TestMarkerCapAcrossModes(t *testing.T) {
	routes := map[string]provider.Route{
		"openrouter": provider.OpenRouterRoute(),
		"gateway":    provider.GatewayRoute("http://127.0.0.1:61890/v1"),
		"unknown":    provider.UncachedRoute("http://127.0.0.1:61890/v1"),
	}
	modes := []provider.MarkMode{provider.MarkAuto, provider.MarkBlocks, provider.MarkTopLevel, provider.MarkNone}
	for name, route := range routes {
		for _, mode := range modes {
			p := newTestProvider(t, route, mode)
			req, err := p.assemble(encodeAll(t, p, conversation()),
				snap(t, map[string]any{"system.credo": "you are a test agent"}), nil, 1024, "aria1")
			if err != nil {
				t.Fatalf("%s/%s assemble: %v", name, mode, err)
			}
			if n := countCacheMarkers(req); n > provider.MaxCacheBreakpoints {
				t.Errorf("%s/%s emitted %d markers, cap is %d", name, mode, n, provider.MaxCacheBreakpoints)
			}
			// Figaro's own ceiling is lower: a downstream router must have a
			// slot left to top up with.
			if n := countCacheMarkers(req); n > provider.AutoCacheBreakpoints {
				t.Errorf("%s/%s emitted %d markers, figaro's budget is %d", name, mode, n, provider.AutoCacheBreakpoints)
			}
		}
	}
}

// --- the default: bare strings plus one request-level directive ------------

func TestGatewayAutoSendsStringsAndOneDirective(t *testing.T) {
	p := newTestProvider(t, provider.GatewayRoute("http://127.0.0.1:61890/v1"), provider.MarkAuto)
	req, err := p.assemble(encodeAll(t, p, conversation()),
		snap(t, map[string]any{"system.credo": "credo"}), nil, 1024, "aria1")
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	if req.CacheControl == nil || req.CacheControl.Type != "ephemeral" {
		t.Fatalf("want one request-level directive, got %+v", req.CacheControl)
	}
	for i, row := range req.Messages {
		m := rowChat(row)
		var s string
		if err := json.Unmarshal(m.Content, &s); err != nil {
			t.Errorf("message %d content is not a bare string: %s", i, m.Content)
		}
	}
}

func TestGatewayBlocksModeMarksSystemAndTailOnly(t *testing.T) {
	p := newTestProvider(t, provider.GatewayRoute("http://127.0.0.1:61890/v1"), provider.MarkBlocks)
	req, err := p.assemble(encodeAll(t, p, conversation()),
		snap(t, map[string]any{"system.credo": "credo"}), nil, 1024, "aria1")
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	if req.CacheControl != nil {
		t.Errorf("per-block mode must not also send a request-level directive")
	}
	marked := markedIndexes(t, req)
	if len(marked) != 2 {
		t.Fatalf("want 2 markers (system + tail), got %d at %v", len(marked), marked)
	}
	if marked[0] != 0 {
		t.Errorf("first marker must be the system prefix, got index %d", marked[0])
	}
	if last := len(req.Messages) - 1; marked[len(marked)-1] != last {
		t.Errorf("the rolling tail must carry the FINAL marker (index %d), got %d, a gateway lowering to Gemini keeps only the last one",
			last, marked[len(marked)-1])
	}
}

func markedIndexes(t *testing.T, req chatRequest) []int {
	t.Helper()
	var out []int
	for i, row := range req.Messages {
		m := rowChat(row)
		var parts []contentPart
		if json.Unmarshal(m.Content, &parts) != nil {
			continue
		}
		for _, p := range parts {
			if p.CacheControl != nil {
				out = append(out, i)
				break
			}
		}
	}
	return out
}

// --- byte stability: shape is a function of route, never of position -------

// Two consecutive turns must serialize the shared prefix identically. If a
// message is a string on one turn and a block list on the next: or carries
// a marker on one turn and not the next: the prefix bytes change and the
// cache it was building is thrown away.
func TestPrefixBytesAreStableAcrossTurns(t *testing.T) {
	for _, mode := range []provider.MarkMode{provider.MarkAuto, provider.MarkBlocks} {
		p := newTestProvider(t, provider.GatewayRoute("http://x/v1"), mode)
		board := snap(t, map[string]any{"system.credo": "credo"})

		turn1 := conversation()
		req1, err := p.assemble(encodeAll(t, p, turn1), board, nil, 1024, "aria1")
		if err != nil {
			t.Fatalf("%s turn 1: %v", mode, err)
		}

		turn2 := append(turn1,
			message.Message{Role: message.RoleOutput, Content: []message.Content{message.TextContent("second reply")}},
			message.Message{Role: message.RoleInput, Content: []message.Content{message.TextContent("third turn")}})
		req2, err := p.assemble(encodeAll(t, p, turn2), board, nil, 1024, "aria1")
		if err != nil {
			t.Fatalf("%s turn 2: %v", mode, err)
		}

		// Everything up to (not including) turn 1's tail is shared prefix.
		shared := len(req1.Messages) - 1
		for i := 0; i < shared; i++ {
			a, _ := json.Marshal(req1.Messages[i])
			b, _ := json.Marshal(req2.Messages[i])
			if string(a) != string(b) {
				t.Errorf("%s: prefix message %d changed between turns\n turn1: %s\n turn2: %s", mode, i, a, b)
			}
		}
	}
}

// --- king's assertion (c) support: one stable session key ------------------

func TestSessionKeyIsSentUnderBothNamesAndIsStable(t *testing.T) {
	p := newTestProvider(t, provider.GatewayRoute("http://x/v1"), provider.MarkAuto)
	board := snap(t, map[string]any{"system.credo": "credo"})
	req1, _ := p.assemble(encodeAll(t, p, conversation()), board, nil, 1024, "aria1")
	req2, _ := p.assemble(encodeAll(t, p, conversation()), board, nil, 1024, "aria1")
	other, _ := p.assemble(encodeAll(t, p, conversation()), board, nil, 1024, "aria2")

	if req1.SessionID == "" || req1.SessionID != req1.PromptCacheKey {
		t.Fatalf("session key must ride under both names: session_id=%q prompt_cache_key=%q",
			req1.SessionID, req1.PromptCacheKey)
	}
	if req1.SessionID != req2.SessionID {
		t.Error("session key must be stable across turns or sticky routing cannot hold")
	}
	if req1.SessionID == other.SessionID {
		t.Error("distinct arias must not share a session key")
	}
	if strings.Contains(req1.SessionID, "aria1") {
		t.Errorf("session key %q leaks the aria id", req1.SessionID)
	}
}

func TestUnknownGatewayIsNeverMarked(t *testing.T) {
	p := newTestProvider(t, provider.UncachedRoute("http://x/v1"), provider.MarkBlocks)
	req, err := p.assemble(encodeAll(t, p, conversation()),
		snap(t, map[string]any{"system.credo": "credo"}), nil, 1024, "aria1")
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	if countCacheMarkers(req) != 0 || req.CacheControl != nil {
		t.Error("an endpoint of unknown provenance must be sent no markers at all")
	}
	if req.SessionID != "" {
		t.Error("no session key for a route that does not advertise sticky routing")
	}
}

// --- king's assertion (b): usage lands in the right buckets ----------------

func TestUsageMappingKeepsContextHonest(t *testing.T) {
	var u chatUsage
	if err := json.Unmarshal([]byte(`{
		"prompt_tokens": 10339,
		"completion_tokens": 60,
		"total_tokens": 10399,
		"prompt_tokens_details": {"cached_tokens": 10318, "cache_write_tokens": 0}
	}`), &u); err != nil {
		t.Fatalf("unmarshal usage: %v", err)
	}
	got := u.toIR()
	if got == nil {
		t.Fatal("usage must map")
	}
	if got.CacheReadTokens != 10318 {
		t.Errorf("cache reads = %d, want 10318", got.CacheReadTokens)
	}
	if got.InputTokens != 21 {
		t.Errorf("uncached input = %d, want 21 (prompt_tokens is INCLUSIVE of cache reads)", got.InputTokens)
	}
	// The whole point: the four buckets must re-sum to the prompt plus the
	// reply, or every cached aria's context figure is wrong.
	total := got.InputTokens + got.CacheReadTokens + got.CacheWriteTokens + got.OutputTokens
	if total != 10399 {
		t.Errorf("ContextFromUsage sum = %d, want total_tokens 10399", total)
	}
}

func TestUsageMappingWithCacheWrites(t *testing.T) {
	u := chatUsage{PromptTokens: 5000, CompletionTokens: 100}
	u.Details.CachedTokens = 1000
	u.Details.CacheWriteTokens = 3500
	got := u.toIR()
	if got.InputTokens != 500 {
		t.Errorf("uncached input = %d, want 500", got.InputTokens)
	}
	if got.CacheWriteTokens != 3500 {
		t.Errorf("cache writes = %d, want 3500", got.CacheWriteTokens)
	}
	if sum := got.InputTokens + got.CacheReadTokens + got.CacheWriteTokens; sum != 5000 {
		t.Errorf("input buckets sum to %d, want prompt_tokens 5000", sum)
	}
}

// A gateway that reports no details at all must not be read as "everything
// was uncached and also cached".
func TestUsageMappingWithoutDetails(t *testing.T) {
	u := chatUsage{PromptTokens: 900, CompletionTokens: 12}
	got := u.toIR()
	if got.InputTokens != 900 || got.CacheReadTokens != 0 || got.CacheWriteTokens != 0 {
		t.Errorf("bare usage mapped to %+v", got)
	}
	if (chatUsage{}).toIR() != nil {
		t.Error("an empty usage block must map to nil, not to a zeroed one")
	}
}

// --- streaming ------------------------------------------------------------

type recordingBus struct {
	text   strings.Builder
	starts []string
	ready  []message.Content
}

func (b *recordingBus) PushDelta(c message.Content)                            { b.text.WriteString(c.Text) }
func (b *recordingBus) PushFigaro(message.Message, ...provider.AssistantCache) {}
func (b *recordingBus) PushToolInvokeStart(id, name string)                    { b.starts = append(b.starts, id+":"+name) }
func (b *recordingBus) PushToolInvokeDelta(string, string)                     {}
func (b *recordingBus) PushToolReady(c message.Content)                        { b.ready = append(b.ready, c) }
func (b *recordingBus) PushMessageEnd(string)                                  {}

func TestDrainSSEAssemblesTextToolsAndUsage(t *testing.T) {
	stream := strings.Join([]string{
		`data: {"choices":[{"delta":{"content":"Largo "}}]}`,
		`data: {"choices":[{"delta":{"content":"al factotum"}}]}`,
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"read","arguments":"{\"path\":"}}]}}]}`,
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"/tmp/x\"}"}}]}}]}`,
		`data: {"choices":[{"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":100,"completion_tokens":7,"prompt_tokens_details":{"cached_tokens":80,"cache_write_tokens":0}}}`,
		"data: [DONE]",
	}, "\n\n") + "\n"

	bus := &recordingBus{}
	got, err := drainSSE(context.Background(), strings.NewReader(stream), bus)
	if err != nil {
		t.Fatalf("drainSSE: %v", err)
	}
	if got.Text != "Largo al factotum" {
		t.Errorf("text = %q", got.Text)
	}
	if bus.text.String() != "Largo al factotum" {
		t.Errorf("deltas pushed to the bus = %q", bus.text.String())
	}
	if len(got.ToolCalls) != 1 || got.ToolCalls[0].Function.Arguments != `{"path":"/tmp/x"}` {
		t.Fatalf("tool call assembled wrong: %+v", got.ToolCalls)
	}
	if len(bus.ready) != 1 || bus.ready[0].Arguments["path"] != "/tmp/x" {
		t.Errorf("tool ready content = %+v", bus.ready)
	}
	if got.Usage == nil || got.Usage.CacheReadTokens != 80 || got.Usage.InputTokens != 20 {
		t.Errorf("usage = %+v", got.Usage)
	}
	msg := got.toIRMessage()
	if msg.StopReason != message.StopToolInvoke {
		t.Errorf("stop reason = %q", msg.StopReason)
	}
}

// Two calls streamed with interleaved fragments must not have their
// arguments concatenated onto each other. Keyed by index, not arrival.
func TestDrainSSEKeepsInterleavedToolCallsApart(t *testing.T) {
	stream := strings.Join([]string{
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"a","function":{"name":"one","arguments":"{\"x\":1"}}]}}]}`,
		`data: {"choices":[{"delta":{"tool_calls":[{"index":1,"id":"b","function":{"name":"two","arguments":"{\"y\":2"}}]}}]}`,
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"}"}}]}}]}`,
		`data: {"choices":[{"delta":{"tool_calls":[{"index":1,"function":{"arguments":"}"}}]}}]}`,
		"data: [DONE]",
	}, "\n\n") + "\n"

	got, err := drainSSE(context.Background(), strings.NewReader(stream), &recordingBus{})
	if err != nil {
		t.Fatalf("drainSSE: %v", err)
	}
	if len(got.ToolCalls) != 2 {
		t.Fatalf("want 2 calls, got %d", len(got.ToolCalls))
	}
	if got.ToolCalls[0].Function.Arguments != `{"x":1}` || got.ToolCalls[1].Function.Arguments != `{"y":2}` {
		t.Errorf("interleaved fragments cross-contaminated: %q / %q",
			got.ToolCalls[0].Function.Arguments, got.ToolCalls[1].Function.Arguments)
	}
}

// --- shape / fingerprint ---------------------------------------------------

// Changing the marking mode changes the wire shape, so it must move the
// fingerprint: otherwise a prefix cached as strings is replayed alongside
// new entries cached as blocks.
func TestFingerprintTracksTheWireShape(t *testing.T) {
	route := provider.GatewayRoute("http://x/v1")
	strings0 := newTestProvider(t, route, provider.MarkAuto).Fingerprint()
	blocks := newTestProvider(t, route, provider.MarkBlocks).Fingerprint()
	if strings0 == blocks {
		t.Fatalf("shape change must move the fingerprint (both %q)", strings0)
	}
	if newTestProvider(t, route, provider.MarkAuto).Fingerprint() != strings0 {
		t.Error("fingerprint must be stable for a given mode")
	}
}

func TestToolResultsEncodeAsToolRole(t *testing.T) {
	p := newTestProvider(t, provider.GatewayRoute("http://x/v1"), provider.MarkAuto)
	msgs := []message.Message{{Role: message.RoleInput, Content: []message.Content{
		message.ToolResultContent("call_1", "read", "file contents", false),
		message.TextContent("and now this"),
	}}}
	encoded := encodeAll(t, p, msgs)
	if len(encoded) != 1 || len(encoded[0]) != 2 {
		t.Fatalf("want a tool message and a user message, got %d entries", len(encoded[0]))
	}
	var tool, user chatMessage
	if err := json.Unmarshal(encoded[0][0], &tool); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(encoded[0][1], &user); err != nil {
		t.Fatal(err)
	}
	if tool.Role != "tool" || tool.ToolCallID != "call_1" {
		t.Errorf("tool result encoded as %+v", tool)
	}
	if user.Role != "user" {
		t.Errorf("prose encoded as %+v", user)
	}
}

// FuzzStampLeafCache: marking must never produce more than one marker on a
// message, never lose the message's text, and never succeed on bare-string
// content (which has nowhere to hang a marker).
func FuzzStampLeafCache(f *testing.F) {
	f.Add(`"hello"`, true)
	f.Add(`[{"type":"text","text":"hello"}]`, true)
	f.Add(`[{"type":"text","text":"a"},{"type":"text","text":"b"}]`, false)
	f.Add(``, true)
	f.Add(`null`, true)
	f.Fuzz(func(t *testing.T, content string, ttl bool) {
		m := chatMessage{Role: "user", Content: json.RawMessage(content)}
		cc := &cacheControl{Type: "ephemeral"}
		if ttl {
			cc.TTL = "1h"
		}
		before := string(m.Content)
		if !stampLeafCache(&m, cc) {
			if string(m.Content) != before {
				t.Fatalf("a failed stamp must not mutate content: %q -> %q", before, m.Content)
			}
			return
		}
		var parts []contentPart
		if err := json.Unmarshal(m.Content, &parts); err != nil {
			t.Fatalf("stamped content is not a part list: %s", m.Content)
		}
		marks := 0
		for _, p := range parts {
			if p.CacheControl != nil {
				marks++
			}
		}
		if marks != 1 {
			t.Fatalf("want exactly one marker, got %d in %s", marks, m.Content)
		}
		if parts[len(parts)-1].CacheControl == nil {
			t.Fatalf("the marker must be on the LAST part: %s", m.Content)
		}
	})
}

// A local gateway holds the real provider credentials itself. Demanding one
// from figaro means inventing a secret that nothing reads, and the CLI's
// credential gate is keyed on the provider name, so it refused to start a
// turn at all ("No provider connected") until one existed. Found by driving
// the real binary in a pty against a local endpoint; no unit test saw it,
// because no unit test went through the credential gate.
func TestAuthOptionalRouteSendsNoAuthorizationHeader(t *testing.T) {
	p := newTestProvider(t, provider.GatewayRoute("http://x/v1"), provider.MarkAuto)
	p.auth = failingResolver{}
	req, err := http.NewRequest("POST", "http://x/v1/chat/completions", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.authorize(req); err != nil {
		t.Fatalf("an auth-optional route must not fail on a missing credential: %v", err)
	}
	if got := req.Header.Get("Authorization"); got != "" {
		t.Errorf("want no Authorization header, got %q", got)
	}
}

// A route that DOES need a credential must still fail loudly rather than
// send an anonymous request and collect a 401 later.
func TestAuthRequiredRouteFailsLoudly(t *testing.T) {
	p := newTestProvider(t, provider.OpenRouterRoute(), provider.MarkAuto)
	p.auth = failingResolver{}
	req, _ := http.NewRequest("POST", "http://x/v1/chat/completions", nil)
	if err := p.authorize(req); err == nil {
		t.Error("openrouter without a credential must error, not send an anonymous request")
	}
}

type failingResolver struct{}

func (failingResolver) Resolve() (string, error) { return "", errNoCredential }
func (failingResolver) Invalidate(string) error  { return nil }

var errNoCredential = errors.New("no credential available")

// A stream that carries no usage block must not be mistaken for a turn
// that used no tokens. Found in the e2e round against a gateway whose
// streaming path returned no usage at all: every bucket read 0 while the
// context figure kept climbing off the estimator, so the aria looked
// accounted for. drainSSE must report nil: not a zeroed Usage: so the
// caller can tell the two apart and say so.
func TestNoUsageBlockIsDistinctFromZeroUsage(t *testing.T) {
	stream := "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
		"data: [DONE]\n\n"
	got, err := drainSSE(context.Background(), strings.NewReader(stream), &recordingBus{})
	if err != nil {
		t.Fatalf("drainSSE: %v", err)
	}
	if got.Usage != nil {
		t.Fatalf("a missing usage block must stay nil, got %+v, a zeroed Usage would fold as a real measurement of zero", got.Usage)
	}
	if msg := got.toIRMessage(); msg.Usage != nil {
		t.Errorf("IR message usage = %+v, want nil", msg.Usage)
	}
}

// And the reverse: an explicit all-zero usage block is also nil, because
// there is nothing to account and folding zeros would be indistinguishable
// from the case above anyway.
func TestExplicitZeroUsageFoldsToNil(t *testing.T) {
	if (chatUsage{}).toIR() != nil {
		t.Error("an empty usage block must map to nil")
	}
}

// The conservative default is correct and was silent: a gateway configured
// with base_url alone gets no markers and no session key. This pins WHAT
// the default is, so a change to it has to be deliberate.
func TestBareBaseURLGatewayIsUncachedAndUnsticky(t *testing.T) {
	route := provider.UncachedRoute("http://127.0.0.1:61890/v1")
	if route.Caps.BlockMarkers || route.Caps.TopLevel {
		t.Error("an endpoint given only a base_url must not be marked")
	}
	if route.Caps.SessionKey {
		t.Error("...and gets no session key either; loosening this is a deliberate change, not a default")
	}
	p := newTestProvider(t, route, provider.MarkAuto)
	req, err := p.assemble(encodeAll(t, p, conversation()),
		snap(t, map[string]any{"system.credo": "credo"}), nil, 1024, "aria1")
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	if req.SessionID != "" || req.PromptCacheKey != "" {
		t.Errorf("session key leaked to an untrusted route: %q/%q", req.SessionID, req.PromptCacheKey)
	}
	if countCacheMarkers(req) != 0 {
		t.Error("markers leaked to an untrusted route")
	}
}

// A gateway that omits `index` on tool_call deltas must not have its calls
// merged. Defaulting the missing index to 0 put every call of a turn in one
// slot and concatenated their arguments; downstream that surfaced as a tool
// invoked with arguments belonging to a different call: or with none at
// all. Twenty turns of a real session were lost to this class of failure.
func TestToolCallsWithoutIndexDoNotMerge(t *testing.T) {
	stream := strings.Join([]string{
		`data: {"choices":[{"delta":{"tool_calls":[{"id":"a","type":"function","function":{"name":"bash","arguments":"{\"command\":"}}]}}]}`,
		`data: {"choices":[{"delta":{"tool_calls":[{"function":{"arguments":"\"ls\"}"}}]}}]}`,
		`data: {"choices":[{"delta":{"tool_calls":[{"id":"b","type":"function","function":{"name":"read","arguments":"{\"path\":"}}]}}]}`,
		`data: {"choices":[{"delta":{"tool_calls":[{"function":{"arguments":"\"x.md\"}"}}]}}]}`,
		`data: {"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
		"data: [DONE]",
	}, "\n\n") + "\n"

	got, err := drainSSE(context.Background(), strings.NewReader(stream), &recordingBus{})
	if err != nil {
		t.Fatalf("drainSSE: %v", err)
	}
	if len(got.ToolCalls) != 2 {
		t.Fatalf("want 2 calls, got %d: %+v", len(got.ToolCalls), got.ToolCalls)
	}
	byName := map[string]string{}
	for _, c := range got.ToolCalls {
		byName[c.Function.Name] = c.Function.Arguments
	}
	if byName["bash"] != `{"command":"ls"}` {
		t.Errorf("bash arguments = %q, want {\"command\":\"ls\"}", byName["bash"])
	}
	if byName["read"] != `{"path":"x.md"}` {
		t.Errorf("read arguments = %q, want {\"path\":\"x.md\"}", byName["read"])
	}
}

// The single-call case a gateway most often emits: no index anywhere, id and
// name on the first fragment only. It must still assemble.
func TestSingleToolCallWithoutIndexAssembles(t *testing.T) {
	stream := strings.Join([]string{
		`data: {"choices":[{"delta":{"tool_calls":[{"id":"a","function":{"name":"bash","arguments":"{\"comm"}}]}}]}`,
		`data: {"choices":[{"delta":{"tool_calls":[{"function":{"arguments":"and\":\"ls -la\"}"}}]}}]}`,
		"data: [DONE]",
	}, "\n\n") + "\n"

	got, err := drainSSE(context.Background(), strings.NewReader(stream), &recordingBus{})
	if err != nil {
		t.Fatalf("drainSSE: %v", err)
	}
	if len(got.ToolCalls) != 1 || got.ToolCalls[0].Function.Arguments != `{"command":"ls -la"}` {
		t.Fatalf("got %+v", got.ToolCalls)
	}
}
