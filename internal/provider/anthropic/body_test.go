package anthropic

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/jack-work/figaro/internal/provider"
)

func rowsOf(bodies ...string) []json.RawMessage {
	rows := make([]json.RawMessage, 0, len(bodies))
	for _, b := range bodies {
		rows = append(rows, json.RawMessage(b))
	}
	return rows
}

// The streamed body must be the SAME BYTES the marshalled one is: prompt cache
// keys are content, and a body that differs from what shipped yesterday is a
// change nobody asked for. Every arm here is a way the two could diverge.
func TestStreamedBodyIsByteIdenticalToMarshal(t *testing.T) {
	cases := []struct {
		name string
		req  nativeRequest
	}{
		{"no messages", nativeRequest{Model: "m", MaxTokens: 8}},
		{"one row", nativeRequest{Model: "m", MaxTokens: 8, Messages: rowsOf(`{"role":"user"}`)}},
		{"html escapable", nativeRequest{Model: "m", MaxTokens: 8, Messages: rowsOf(
			`{"role":"user","content":"a < b && c > d"}`,
			`{"role":"assistant","content":"<script>alert(1)</script>"}`,
		)}},
		{"line separators", nativeRequest{Model: "m", MaxTokens: 8, Messages: rowsOf(
			"{\"role\":\"user\",\"content\":\"a\u2028b\u2029c\"}",
		)}},
		{"whitespace in the row", nativeRequest{Model: "m", MaxTokens: 8, Messages: rowsOf(
			"{\n  \"role\": \"user\",\n  \"content\": [ ]\n}",
		)}},
		{"the split token inside the system prompt", nativeRequest{
			Model: "m", MaxTokens: 8,
			System:   []systemBlock{{Type: "text", Text: `"messages":null and <b>`}},
			Messages: rowsOf(`{"role":"user"}`),
		}},
		{"tools and thinking after the messages", nativeRequest{
			Model: "m", MaxTokens: 8,
			Messages: rowsOf(`{"role":"user"}`),
			Tools:    []nativeTool{{Name: "bash", Description: "run <a command>"}},
			Stream:   true,
			Thinking: &thinkingParam{Type: "enabled", BudgetTokens: 1024},
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			want, err := json.Marshal(tc.req)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			var got bytes.Buffer
			if err := bodyFunc(tc.req)(&got); err != nil {
				t.Fatalf("stream: %v", err)
			}
			if !bytes.Equal(got.Bytes(), want) {
				t.Fatalf("streamed body differs\n got: %s\nwant: %s", got.Bytes(), want)
			}
		})
	}
}

func FuzzStreamedBodyIsByteIdenticalToMarshal(f *testing.F) {
	f.Add("hello", "a < b", "text")
	f.Add(`"messages":null`, "\u2028", "")
	f.Fuzz(func(t *testing.T, system, text, tool string) {
		body, err := json.Marshal(map[string]any{"role": "user", "content": text})
		if err != nil {
			t.Skip()
		}
		req := nativeRequest{
			Model: "m", MaxTokens: 8,
			System:   []systemBlock{{Type: "text", Text: system}},
			Messages: []json.RawMessage{body, body},
			Tools:    []nativeTool{{Name: tool, Description: tool}},
		}
		want, err := json.Marshal(req)
		if err != nil {
			t.Skip()
		}
		var got bytes.Buffer
		if err := bodyFunc(req)(&got); err != nil {
			t.Fatalf("stream: %v", err)
		}
		if !bytes.Equal(got.Bytes(), want) {
			t.Fatalf("streamed body differs\n got: %s\nwant: %s", got.Bytes(), want)
		}
	})
}

func benchRequest(n int) nativeRequest {
	req := nativeRequest{
		Model: "claude-sonnet-4-5", MaxTokens: 8192, Stream: true,
		System: []systemBlock{{Type: "text", Text: "you are a factotum"}},
	}
	for i := 0; i < n; i++ {
		body, err := json.Marshal(map[string]any{
			"role": "user",
			"content": []map[string]any{{
				"type": "text",
				"text": "turn body " + strconv.Itoa(i) + " with enough text to be a plausible message on the wire",
			}},
		})
		if err != nil {
			panic(err)
		}
		req.Messages = append(req.Messages, body)
	}
	return req
}

// The quantity is B/op: the marshalled body is a contiguous copy of the whole
// conversation, the streamed one is a frame plus one row.
func BenchmarkRequestBodyMarshalled(b *testing.B) {
	for _, n := range []int{1_000, 10_000} {
		b.Run(strconv.Itoa(n), func(b *testing.B) {
			req := benchRequest(n)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				body, err := json.Marshal(req)
				if err != nil {
					b.Fatal(err)
				}
				if len(body) == 0 {
					b.Fatal("empty body")
				}
			}
		})
	}
}

func BenchmarkRequestBodyStreamed(b *testing.B) {
	for _, n := range []int{1_000, 10_000} {
		b.Run(strconv.Itoa(n), func(b *testing.B) {
			req := benchRequest(n)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if err := bodyFunc(req)(countingWriter{}); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

type countingWriter struct{}

func (countingWriter) Write(p []byte) (int, error) { return len(p), nil }

// The transient retry rebuilds the request per attempt, so a streamed body has
// to be re-walked rather than consumed once. The canary is a body that is
// attached before the loop instead of inside it: the second attempt then sends
// nothing.
func TestATransientRetryResendsTheWholeStreamedBody(t *testing.T) {
	old := retryBaseDelay
	retryBaseDelay = time.Millisecond
	defer func() { retryBaseDelay = old }()

	var mu sync.Mutex
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		seen = append(seen, string(b))
		n := len(seen)
		mu.Unlock()
		if n == 1 {
			w.WriteHeader(529)
			return
		}
		w.WriteHeader(200)
	}))
	defer srv.Close()

	req := nativeRequest{Model: "m", MaxTokens: 8, Messages: rowsOf(
		`{"role":"user","content":"a < b"}`,
		`{"role":"assistant","content":"ok"}`,
	)}
	body, err := provider.NewRequestBody(bodyFunc(req), true)
	if err != nil {
		t.Fatal(err)
	}
	a := &Anthropic{auth: &staticAuth{token: "t"}, HTTPClient: srv.Client()}
	resp, _, err := a.doWithAuthRetry(context.Background(), func(string) (*http.Request, error) {
		httpReq, herr := http.NewRequest("POST", srv.URL, nil)
		if herr != nil {
			return nil, herr
		}
		body.Attach(httpReq)
		return httpReq, nil
	})
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	resp.Body.Close()

	want, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(seen) != 2 {
		t.Fatalf("server saw %d requests, want 2", len(seen))
	}
	for i, got := range seen {
		if got != string(want) {
			t.Fatalf("attempt %d sent %q, want %q", i+1, got, want)
		}
	}
}
