package chat_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/transport"
	"github.com/jack-work/figaro/internal/testfence"
	"github.com/jack-work/figaro/internal/testreap"
)

func TestMain(m *testing.M) {
	reap := testreap.Start()
	testfence.Install()
	code := m.Run()
	if binDir != "" {
		os.RemoveAll(binDir)
	}
	os.Exit(reap.Check(code))
}

var (
	binOnce sync.Once
	binDir  string
	binPath string
	binErr  error
)

func figaroBinary(t *testing.T) string {
	t.Helper()
	binOnce.Do(func() {
		if bin := os.Getenv("FIGARO_SMOKE_BIN"); bin != "" {
			binPath = bin
			return
		}
		binDir, binErr = os.MkdirTemp("", "figaro-chat-bin-")
		if binErr != nil {
			return
		}
		binPath = filepath.Join(binDir, "figaro")
		out, err := exec.Command("go", "build", "-o", binPath, "github.com/jack-work/figaro/cmd/figaro").CombinedOutput()
		if err != nil {
			binErr = fmt.Errorf("go build: %v\n%s", err, out)
		}
	})
	if binErr != nil {
		t.Fatal(binErr)
	}
	return binPath
}

// stubProvider speaks the Anthropic messages stream. The last user text picks
// the reply: "words N MS" streams N words MS apart, "tool" calls bash once and
// then answers, "fail" is refused, anything else answers "ok".
type stubProvider struct {
	srv *httptest.Server
}

func newStubProvider(t *testing.T) *stubProvider {
	t.Helper()
	p := &stubProvider{}
	p.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/messages") {
			http.NotFound(w, r)
			return
		}
		body, _ := io.ReadAll(r.Body)
		if last, _ := lastUser(body); strings.HasPrefix(last, "fail") {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			io.WriteString(w, `{"type":"error","error":{"type":"invalid_request_error","message":"stub refused"}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		s := &sse{w: w, f: w.(http.Flusher), ctx: r.Context()}
		last, toolResult := lastUser(body)
		switch {
		case toolResult:
			s.text([]string{"The ", "tool ", "said ", "hi."}, 0)
		case strings.HasPrefix(last, "tool"):
			s.tool("toolu_1", "bash", `{"command":"echo hi"}`)
		case strings.HasPrefix(last, "words "):
			var n, ms int
			fmt.Sscanf(last, "words %d %d", &n, &ms)
			words := make([]string, n)
			for i := range words {
				words[i] = fmt.Sprintf(" w%d", i)
			}
			if n > 0 {
				words[0] = "w0"
			}
			s.text(words, time.Duration(ms)*time.Millisecond)
		default:
			s.text([]string{"ok"}, 0)
		}
	}))
	t.Cleanup(p.srv.Close)
	return p
}

type sse struct {
	w   io.Writer
	f   http.Flusher
	ctx context.Context
}

func (s *sse) event(name, data string) {
	fmt.Fprintf(s.w, "event: %s\ndata: %s\n\n", name, data)
	s.f.Flush()
}

func (s *sse) open(block string) {
	s.event("message_start", `{"type":"message_start","message":{"usage":{"input_tokens":5}}}`)
	s.event("content_block_start", `{"type":"content_block_start","index":0,"content_block":`+block+`}`)
}

func (s *sse) close(stop string) {
	s.event("content_block_stop", `{"type":"content_block_stop","index":0}`)
	s.event("message_delta", `{"type":"message_delta","delta":{"stop_reason":"`+stop+`"},"usage":{"output_tokens":3}}`)
	s.event("message_stop", `{"type":"message_stop"}`)
}

func (s *sse) text(chunks []string, gap time.Duration) {
	s.open(`{"type":"text"}`)
	for _, c := range chunks {
		q, _ := json.Marshal(c)
		s.event("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":`+string(q)+`}}`)
		select {
		case <-time.After(gap):
		case <-s.ctx.Done():
			return
		}
	}
	s.close("end_turn")
}

func (s *sse) tool(id, name, args string) {
	s.open(`{"type":"tool_use","id":"` + id + `","name":"` + name + `"}`)
	q, _ := json.Marshal(args)
	s.event("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":`+string(q)+`}}`)
	s.close("tool_use")
}

func lastUser(body []byte) (text string, toolResult bool) {
	var req struct {
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if json.Unmarshal(body, &req) != nil {
		return "", false
	}
	for i := len(req.Messages) - 1; i >= 0; i-- {
		m := req.Messages[i]
		if m.Role != "user" {
			continue
		}
		var s string
		if json.Unmarshal(m.Content, &s) == nil {
			return s, false
		}
		var blocks []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		_ = json.Unmarshal(m.Content, &blocks)
		for _, b := range blocks {
			if b.Type == "tool_result" {
				toolResult = true
			}
			if b.Text != "" && !strings.HasPrefix(b.Text, "<system-reminder") {
				text = b.Text
			}
		}
		return strings.TrimSpace(text), toolResult
	}
	return "", false
}

type daemon struct {
	bin  string
	env  []string
	sock string
}

func newDaemon(t *testing.T) *daemon {
	t.Helper()
	if testing.Short() {
		t.Skip("spawns a figaro daemon; skipped under -short")
	}
	prov := newStubProvider(t)
	root, err := os.MkdirTemp("/var/tmp", "figaro-chat-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	for _, sub := range []string{"run", "state", "cache", "config/outfits", "hush"} {
		if err := os.MkdirAll(filepath.Join(root, sub), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	write := func(rel, body string) {
		if err := os.WriteFile(filepath.Join(root, rel), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("config/config.toml", "default_outfit = \"default\"\n")
	write("config/outfits/default.toml", "[system]\nprovider = \"anthropic\"\nmodel = \"claude-sonnet-4-5\"\n")
	var env []string
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(k, "FIGARO_") || strings.HasPrefix(k, "ANTHROPIC_") || strings.HasPrefix(k, "HUSH_") {
			continue
		}
		env = append(env, kv)
	}
	d := &daemon{bin: figaroBinary(t), sock: filepath.Join(root, "run", "angelus.sock"), env: append(env,
		"FIGARO_RUNTIME_DIR="+filepath.Join(root, "run"),
		"FIGARO_NO_ANGELUS_SCOPE=1",
		"HUSH_NO_AGENT_SCOPE=1",
		"FIGARO_STATE_DIR="+filepath.Join(root, "state"),
		"FIGARO_CACHE_DIR="+filepath.Join(root, "cache"),
		"FIGARO_CONFIG_DIR="+filepath.Join(root, "config"),
		"FIGARO_HUSH_APP=figaro-test-chat",
		"FIGARO_HUSH_DIR="+filepath.Join(root, "hush"),
		"FIGARO_HUSH_PASSPHRASE=figaro-chat-e2e",
		"FIGARO_HUSH_TTL=2m",
		"FIGARO_NO_BIND=1",
		"ANTHROPIC_API_KEY=sk-ant-held",
		"ANTHROPIC_BASE_URL="+prov.srv.URL,
		testreap.Scope(t),
	)}
	t.Cleanup(func() { _, _ = d.cmd("stop", "--force").CombinedOutput() })
	return d
}

func (d *daemon) cmd(args ...string) *exec.Cmd {
	c := exec.Command(d.bin, args...)
	c.Env = d.env
	return c
}

func (d *daemon) must(t *testing.T, args ...string) string {
	t.Helper()
	out, err := d.cmd(args...).CombinedOutput()
	if err != nil {
		t.Fatalf("figaro %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

func (d *daemon) endpoint() transport.Endpoint { return transport.UnixEndpoint(d.sock) }

// root mints the aria threads fork from, granting it children.
func (d *daemon) root(t *testing.T) string {
	t.Helper()
	out := d.must(t, "new", "-j", "--", "hello")
	lines := strings.Split(strings.TrimSpace(out), "\n")
	var created struct {
		AriaID string `json:"aria_id"`
	}
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &created); err != nil || created.AriaID == "" {
		t.Fatalf("new -j printed no aria id: %q", out)
	}
	d.must(t, "set", "--id", created.AriaID, "system.fanout.max_children", "16")
	return created.AriaID
}
