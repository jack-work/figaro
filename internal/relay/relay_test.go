package relay

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jack-work/figaro/internal/auth"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/provider/routed"
)

type rotatingToken struct {
	mu          sync.Mutex
	tokens      []string
	invalidated []string
}

func (r *rotatingToken) Resolve() (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.tokens[0], nil
}

func (r *rotatingToken) Invalidate(tok string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.invalidated = append(r.invalidated, tok)
	if len(r.tokens) > 1 && r.tokens[0] == tok {
		r.tokens = r.tokens[1:]
	}
	return nil
}

type fakeUpstream struct {
	base    string
	headers map[routed.Transport]map[string]string
	cred    auth.TokenResolver
	models  []provider.ModelInfo
}

func (f *fakeUpstream) Base() string { return f.base }

func (f *fakeUpstream) Endpoint(t routed.Transport) string {
	switch t {
	case routed.Messages:
		return f.base + "/v1/messages"
	case routed.Chat:
		return f.base + "/chat/completions"
	}
	return f.base + "/responses"
}

func (f *fakeUpstream) Headers(t routed.Transport) map[string]string { return f.headers[t] }

func (f *fakeUpstream) Credential() auth.TokenResolver { return f.cred }

func (f *fakeUpstream) Models(context.Context) ([]provider.ModelInfo, error) { return f.models, nil }

type seen struct {
	method, path, query, auth, apiKey, cookie, custom, transportHeader string
	body                                                               []byte
}

func recorder(t *testing.T, got chan<- seen, respond func(w http.ResponseWriter, r *http.Request)) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		s := seen{
			method: r.Method, path: r.URL.Path, query: r.URL.RawQuery,
			auth: r.Header.Get("Authorization"), apiKey: r.Header.Get("X-Api-Key"),
			cookie: r.Header.Get("Cookie"), custom: r.Header.Get("X-Static"),
			transportHeader: r.Header.Get("X-Transport"), body: body,
		}
		select {
		case got <- s:
		default:
			t.Errorf("upstream received more requests than the test expects: %s %s", r.Method, r.URL.Path)
		}
		respond(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func front(t *testing.T, ups map[string]Upstream) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(New(ups, nil).Handler())
	t.Cleanup(srv.Close)
	return srv
}

func ok(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, `{"ok":true}`) }

// The passthrough door must reach base+path with the provider's credential
// and headers, and nothing of the caller's own credentials.
func TestPassthroughSwapsCredentialAndKeepsPath(t *testing.T) {
	got := make(chan seen, 1)
	up := recorder(t, got, ok)
	fr := front(t, map[string]Upstream{"meta": &fakeUpstream{
		base: up.URL + "/v1",
		headers: map[routed.Transport]map[string]string{
			routed.Chat: {"X-Static": "s", "X-Transport": "chat"},
		},
		cred: &rotatingToken{tokens: []string{"real-key"}},
	}})

	req, _ := http.NewRequest("POST", fr.URL+"/u/meta/chat/completions?x=1", strings.NewReader(`{"model":"m"}`))
	req.Header.Set("Authorization", "Bearer caller-key")
	req.Header.Set("X-Api-Key", "caller-key")
	req.Header.Set("Cookie", "session=1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	s := <-got
	if s.path != "/v1/chat/completions" || s.query != "x=1" {
		t.Errorf("upstream saw %s?%s", s.path, s.query)
	}
	if s.auth != "Bearer real-key" || s.apiKey != "" || s.cookie != "" {
		t.Errorf("credentials leaked or missing: auth=%q x-api-key=%q cookie=%q", s.auth, s.apiKey, s.cookie)
	}
	if s.custom != "s" || s.transportHeader != "chat" {
		t.Errorf("provider headers missing: %+v", s)
	}
	if string(s.body) != `{"model":"m"}` {
		t.Errorf("body %s", s.body)
	}
}

// The /v1 door picks the provider from the model prefix, strips it, and posts
// to that provider's endpoint for the transport the path names.
func TestModelPrefixPicksProviderAndIsStripped(t *testing.T) {
	gotA, gotB := make(chan seen, 1), make(chan seen, 1)
	a := recorder(t, gotA, ok)
	b := recorder(t, gotB, ok)
	fr := front(t, map[string]Upstream{
		"meta":    &fakeUpstream{base: a.URL + "/v1", cred: &rotatingToken{tokens: []string{"ka"}}},
		"copilot": &fakeUpstream{base: b.URL, cred: &rotatingToken{tokens: []string{"kb"}}},
	})

	resp, err := http.Post(fr.URL+"/v1/responses", "application/json",
		strings.NewReader(`{"model":"copilot/gpt-6-astra","input":"hi","stream":true}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	select {
	case s := <-gotA:
		t.Fatalf("meta received a copilot request: %s", s.body)
	case s := <-gotB:
		if s.path != "/responses" || s.auth != "Bearer kb" {
			t.Errorf("copilot saw %s auth=%q", s.path, s.auth)
		}
		var body map[string]any
		json.Unmarshal(s.body, &body)
		if body["model"] != "gpt-6-astra" || body["input"] != "hi" || body["stream"] != true {
			t.Errorf("rewritten body %s", s.body)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no upstream was reached")
	}

	resp, err = http.Post(fr.URL+"/v1/messages", "application/json", strings.NewReader(`{"model":"meta/m"}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if s := <-gotA; s.path != "/v1/v1/messages" {
		t.Errorf("messages went to %s, want the provider's messages endpoint", s.path)
	}
}

func TestModelWithoutKnownPrefixIsRefused(t *testing.T) {
	fr := front(t, map[string]Upstream{"meta": &fakeUpstream{base: "http://127.0.0.1:1", cred: &rotatingToken{tokens: []string{"k"}}}})
	for body, want := range map[string]int{
		`{"model":"muse-wonder-1.0"}`: 400,
		`{"model":"nobody/muse"}`:     404,
		`{"input":"no model"}`:        400,
		`not json`:                    400,
	} {
		resp, err := http.Post(fr.URL+"/v1/responses", "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		msg, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != want {
			t.Errorf("%s: status %d, want %d (%s)", body, resp.StatusCode, want, msg)
		}
	}
}

// A 401 means the credential went stale: it is invalidated, a fresh one is
// resolved, and the same body is replayed once.
func TestUnauthorizedInvalidatesAndRetriesOnce(t *testing.T) {
	got := make(chan seen, 4)
	up := recorder(t, got, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer fresh" {
			http.Error(w, "stale", http.StatusUnauthorized)
			return
		}
		ok(w, r)
	})
	cred := &rotatingToken{tokens: []string{"stale", "fresh"}}
	fr := front(t, map[string]Upstream{"copilot": &fakeUpstream{base: up.URL, cred: cred}})

	resp, err := http.Post(fr.URL+"/u/copilot/chat/completions", "application/json", strings.NewReader(`{"a":1}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status %d after retry", resp.StatusCode)
	}
	first, second := <-got, <-got
	if first.auth != "Bearer stale" || second.auth != "Bearer fresh" || string(second.body) != `{"a":1}` {
		t.Errorf("attempts: %+v then %+v", first, second)
	}
	if len(cred.invalidated) != 1 || cred.invalidated[0] != "stale" {
		t.Errorf("invalidated %v", cred.invalidated)
	}
}

// Streaming is the point of a model relay: the first event must reach the
// caller while the upstream is still holding the stream open.
func TestStreamFlushesBeforeUpstreamFinishes(t *testing.T) {
	release := make(chan struct{})
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: first\n\n")
		w.(http.Flusher).Flush()
		<-release
		io.WriteString(w, "data: last\n\n")
	}))
	defer up.Close()
	defer close(release)
	fr := front(t, map[string]Upstream{"meta": &fakeUpstream{base: up.URL, cred: &rotatingToken{tokens: []string{"k"}}}})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	line := make(chan string, 1)
	go func() {
		req, _ := http.NewRequestWithContext(ctx, "POST", fr.URL+"/u/meta/responses", strings.NewReader(`{}`))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			line <- "error: " + err.Error()
			return
		}
		defer resp.Body.Close()
		l, _ := bufio.NewReader(resp.Body).ReadString('\n')
		line <- l
	}()
	select {
	case l := <-line:
		if l != "data: first\n" {
			t.Errorf("first line %q", l)
		}
	case <-time.After(3 * time.Second):
		t.Error("first event was buffered until the upstream finished")
	}
}

// Unauthenticated is tolerable only on loopback: a rebinding name in Host,
// or a page on another origin, must not be able to spend the credentials.
func TestNonLoopbackHostAndForeignOriginAreRefused(t *testing.T) {
	got := make(chan seen, 2)
	up := recorder(t, got, ok)
	fr := front(t, map[string]Upstream{"meta": &fakeUpstream{base: up.URL, cred: &rotatingToken{tokens: []string{"k"}}}})

	cases := []struct {
		name, host, origin string
		want               int
	}{
		{"loopback", "", "", 200},
		{"localhost origin", "", "http://localhost:3000", 200},
		{"rebinding host", "evil.example:8788", "", 403},
		{"foreign origin", "", "https://evil.example", 403},
		{"null origin", "", "null", 403},
	}
	for _, c := range cases {
		req, _ := http.NewRequest("POST", fr.URL+"/u/meta/responses", strings.NewReader(`{}`))
		if c.host != "" {
			req.Host = c.host
		}
		if c.origin != "" {
			req.Header.Set("Origin", c.origin)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != c.want {
			t.Errorf("%s: status %d, want %d", c.name, resp.StatusCode, c.want)
		}
	}
	if n := len(got); n != 2 {
		t.Errorf("upstream reached %d times, want only the 2 permitted requests", n)
	}
}

func TestListenRefusesNonLoopback(t *testing.T) {
	for _, addr := range []string{"0.0.0.0:0", ":0", "192.0.2.1:0"} {
		if ln, err := ListenLoopback(addr); err == nil {
			ln.Close()
			t.Errorf("%s: listened", addr)
		}
	}
	ln, err := ListenLoopback("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ln.Close()
}

func TestModelsArePrefixedByProvider(t *testing.T) {
	fr := front(t, map[string]Upstream{
		"meta":    &fakeUpstream{cred: &rotatingToken{tokens: []string{"k"}}, models: []provider.ModelInfo{{ID: "muse-wonder-1.0"}}},
		"copilot": &fakeUpstream{cred: &rotatingToken{tokens: []string{"k"}}, models: []provider.ModelInfo{{ID: "gpt-6-astra", ContextWindow: 400000}}},
	})
	resp, err := http.Get(fr.URL + "/v1/models")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var list struct {
		Data []modelRow `json:"data"`
	}
	json.NewDecoder(resp.Body).Decode(&list)
	ids := []string{}
	for _, m := range list.Data {
		ids = append(ids, m.ID)
	}
	if strings.Join(ids, ",") != "copilot/gpt-6-astra,meta/muse-wonder-1.0" {
		t.Errorf("ids %v", ids)
	}
	if list.Data[0].ContextWindow != 400000 {
		t.Errorf("context window dropped: %+v", list.Data[0])
	}
}

// The decisions wire takes a closed body (model, state, questions): the
// endpoint refuses the whole request on any member it does not know. The
// relay may rewrite the model and nothing else.
func TestSystemOneRoutesByModelPrefix(t *testing.T) {
	got := make(chan seen, 1)
	up := recorder(t, got, ok)
	fr := front(t, map[string]Upstream{"gateway": &fakeUpstream{
		base: up.URL + "/v1",
		cred: &rotatingToken{tokens: []string{"k"}},
	}})
	in := `{"model":"gateway/explabs/jev-latest","state":"s","questions":{"q":{"type":"noul","instructions":"i"}}}`
	resp, err := http.Post(fr.URL+"/v1/systemone", "application/json", strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	s := within(t, got)
	if s.path != "/v1/systemone" || s.auth != "Bearer k" {
		t.Fatalf("upstream saw %s auth=%q", s.path, s.auth)
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(s.body, &body); err != nil {
		t.Fatal(err)
	}
	if string(body["model"]) != `"explabs/jev-latest"` {
		t.Errorf("model = %s: only the first segment names the provider", body["model"])
	}
	for k := range body {
		switch k {
		case "model", "state", "questions":
		default:
			t.Errorf("the relay added %q to a closed body", k)
		}
	}
}

func TestDecisionsUpstreamIsTheSystemOneHost(t *testing.T) {
	got := make(chan seen, 2)
	up := recorder(t, got, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			io.WriteString(w, `{"models":[{"name":"jev-latest"}],"data":[{"id":"jev-1.13"},{"id":"gpt-6-luna"}]}`)
			return
		}
		ok(w, r)
	})
	d, err := newDecisions(up.URL+"/v1", &rotatingToken{tokens: []string{"ts-key"}}, false)
	if err != nil {
		t.Fatal(err)
	}
	if d.Endpoint(routed.Chat) != up.URL+"/v1/systemone" {
		t.Errorf("endpoint = %s", d.Endpoint(routed.Chat))
	}
	fr := front(t, map[string]Upstream{"decisions": d})

	resp, err := http.Post(fr.URL+"/v1/systemone", "application/json", strings.NewReader(`{"model":"decisions/jev-latest","state":"s","questions":{}}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	if s := within(t, got); s.path != "/v1/systemone" || s.auth != "Bearer ts-key" || !strings.Contains(string(s.body), `"model":"jev-latest"`) {
		t.Errorf("upstream saw %s auth=%q body=%s", s.path, s.auth, s.body)
	}

	models, err := d.Models(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	within(t, got)
	var ids []string
	for _, m := range models {
		ids = append(ids, m.ID)
	}
	if strings.Join(ids, ",") != "jev-latest,jev-1.13" {
		t.Errorf("models = %v: both catalog shapes, and only decision models from an OpenAI-shaped list", ids)
	}
}

func TestDecisionsNeedsAKeyOnlyWhereTheHostDoes(t *testing.T) {
	none := &auth.Aggregate{}
	if _, err := newDecisions("https://api.typesafe.ai/v1", none, false); err == nil {
		t.Error("TypeSafe with no key was served; every request would come back 401")
	}
	d, err := newDecisions("http://127.0.0.1:8787/v1", none, true)
	if err != nil {
		t.Fatalf("a broker that holds the key was refused: %v", err)
	}
	if tok, err := d.Credential().Resolve(); err != nil || tok != "" {
		t.Errorf("broker credential = %q, %v: want no header and no error", tok, err)
	}
}

func within(t *testing.T, got <-chan seen) seen {
	t.Helper()
	select {
	case s := <-got:
		return s
	case <-time.After(5 * time.Second):
		t.Fatal("no upstream was reached")
		return seen{}
	}
}
