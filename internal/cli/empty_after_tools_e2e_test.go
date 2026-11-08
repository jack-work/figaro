package cli

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/jack-work/figaro/api/message"
)

// gen5 found it: a henchman told to write nothing after its last command
// ends the turn with an empty end_turn completion that follows a tool
// result, and figaro sealed that turn as an error. Silence after tools is
// the end of the turn; silence as the first reply is still a fault.

func sseEmptyEndTurn() string {
	return "event: message_start\n" +
		`data: {"type":"message_start","message":{"usage":{"input_tokens":5}}}` + "\n\n" +
		"event: message_delta\n" +
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}` + "\n\n" +
		"event: message_stop\n" +
		`data: {"type":"message_stop"}` + "\n\n"
}

type silentProvider struct {
	srv    *httptest.Server
	mu     sync.Mutex
	bodies [][]byte
}

func newSilentProvider(t *testing.T) *silentProvider {
	t.Helper()
	p := &silentProvider{}
	p.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/messages") {
			http.NotFound(w, r)
			return
		}
		body, _ := io.ReadAll(r.Body)
		p.mu.Lock()
		p.bodies = append(p.bodies, body)
		p.mu.Unlock()
		last := lastUserText(body)
		var sse string
		switch {
		case strings.Contains(last, "next prompt"):
			sse = sseText("ack")
		case strings.Contains(last, "MARK-ran"):
			sse = sseEmptyEndTurn()
		case strings.Contains(last, "run it and stop"):
			sse = sseBash("toolu_quiet", "echo MARK-ran")
		case strings.Contains(last, "say nothing"):
			sse = sseEmptyEndTurn()
		default:
			sse = sseText("ack")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, sse)
	}))
	t.Cleanup(p.srv.Close)
	return p
}

func (p *silentProvider) last() []byte {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.bodies[len(p.bodies)-1]
}

func turnDoneReason(t *testing.T, rig *sendRig, aria, prompt string) string {
	t.Helper()
	var stderr bytes.Buffer
	c := rig.cmd("send", "-v", "--id", aria, "--", prompt)
	c.Stderr = &stderr
	out, _ := c.Output()
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 1<<20), 1<<24)
	for sc.Scan() {
		var frame struct {
			Method string `json:"method"`
			Params struct {
				Reason string `json:"reason"`
			} `json:"params"`
		}
		if json.Unmarshal(sc.Bytes(), &frame) == nil && frame.Method == "turn.done" {
			return frame.Params.Reason
		}
	}
	t.Fatalf("send %q printed no turn.done:\n%s\nstderr: %s", prompt, out, stderr.String())
	return ""
}

func freshAria(t *testing.T, rig *sendRig) string {
	t.Helper()
	out, err := rig.cmd("new", "-j").Output()
	if err != nil {
		t.Fatalf("new -j: %v\n%s", err, out)
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	var created struct {
		AriaID string `json:"aria_id"`
	}
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &created); err != nil || created.AriaID == "" {
		t.Fatalf("new -j printed no aria id: %q", out)
	}
	return created.AriaID
}

func TestAnEmptyEndTurnAfterToolResultsEndsTheTurnCleanly(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns a daemon; skipped under -short")
	}
	prov := newSilentProvider(t)
	rig := newSendRig(t, prov.srv.URL)
	t.Cleanup(func() { _ = rig.cmd("stop").Run() })
	aria := freshAria(t, rig)

	clean := string(message.StopEnd)
	if reason := turnDoneReason(t, rig, aria, "run it and stop"); reason != clean {
		t.Fatalf("a turn whose model says nothing after its last tool result sealed as %q, want %q", reason, clean)
	}
	if reason := turnDoneReason(t, rig, aria, "next prompt"); reason != clean {
		t.Fatalf("the turn after it sealed as %q, want %q", reason, clean)
	}
	var req struct {
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(prov.last(), &req); err != nil {
		t.Fatal(err)
	}
	for i, m := range req.Messages {
		if m.Role == "assistant" && (len(m.Content) == 0 || string(m.Content) == "[]" || string(m.Content) == `""` || string(m.Content) == "null") {
			t.Fatalf("message %d of the next request is an empty assistant message, which the API rejects: %s", i, prov.last())
		}
		if i > 0 && m.Role == req.Messages[i-1].Role {
			t.Fatalf("messages %d and %d are both %s: %s", i-1, i, m.Role, prov.last())
		}
	}
	if !strings.Contains(string(prov.last()), "MARK-ran") || !strings.Contains(string(prov.last()), "next prompt") {
		t.Fatalf("the next request lost the tool result or the new prompt: %s", prov.last())
	}
}

func TestAnEmptyFirstReplyIsStillAFault(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns a daemon; skipped under -short")
	}
	prov := newSilentProvider(t)
	rig := newSendRig(t, prov.srv.URL)
	t.Cleanup(func() { _ = rig.cmd("stop").Run() })
	aria := freshAria(t, rig)

	reason := turnDoneReason(t, rig, aria, "say nothing")
	if !strings.HasPrefix(reason, "error:") || !strings.Contains(reason, "empty completion") {
		t.Fatalf("an empty first reply sealed as %q, want the empty-completion error", reason)
	}
}
