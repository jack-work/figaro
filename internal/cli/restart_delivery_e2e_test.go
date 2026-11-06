package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jack-work/figaro/internal/figaro"
)

// A daemon that stops, crashes or restarts must not lose what was sent to an
// aria, and must not leave a turn it cut off waiting for a keypress. Each test
// drives the real binary against a scripted provider and restarts the daemon
// under it.

type scriptedReply func(last string) (hold bool, sse string)

type scriptedProvider struct {
	srv   *httptest.Server
	mu    sync.Mutex
	asks  []string
	reply scriptedReply
	seen  chan struct{}
}

func newScriptedProvider(t *testing.T, reply scriptedReply) *scriptedProvider {
	t.Helper()
	p := &scriptedProvider{reply: reply, seen: make(chan struct{}, 64)}
	p.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/messages") {
			http.NotFound(w, r)
			return
		}
		body, _ := io.ReadAll(r.Body)
		last := lastUserText(body)
		p.mu.Lock()
		p.asks = append(p.asks, last)
		p.mu.Unlock()
		select {
		case p.seen <- struct{}{}:
		default:
		}
		hold, sse := p.reply(last)
		if hold {
			<-r.Context().Done()
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, sse)
	}))
	t.Cleanup(p.srv.Close)
	return p
}

func (p *scriptedProvider) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.asks)
}

func (p *scriptedProvider) ask(i int) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.asks[i]
}

// awaitAsk waits for a request after the first `after` whose last user message
// contains every one of want, and returns its index.
func (p *scriptedProvider) awaitAsk(t *testing.T, after int, within time.Duration, want ...string) int {
	t.Helper()
	deadline := time.Now().Add(within)
	for {
		p.mu.Lock()
		for i := after; i < len(p.asks); i++ {
			ok := true
			for _, w := range want {
				ok = ok && strings.Contains(p.asks[i], w)
			}
			if ok {
				p.mu.Unlock()
				return i
			}
		}
		asks := append([]string(nil), p.asks...)
		p.mu.Unlock()
		if time.Now().After(deadline) {
			t.Fatalf("no request carrying %q within %s; requests seen: %q", want, within, asks)
		}
		select {
		case <-p.seen:
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// expectQuiet fails if any request beyond the first n arrives within d.
func (p *scriptedProvider) expectQuiet(t *testing.T, n int, d time.Duration, why string) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if c := p.count(); c > n {
			t.Fatalf("%s: request %d arrived: %q", why, c, p.ask(n))
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func lastUserText(body []byte) string {
	var req struct {
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if json.Unmarshal(body, &req) != nil {
		return ""
	}
	for i := len(req.Messages) - 1; i >= 0; i-- {
		m := req.Messages[i]
		if m.Role != "user" {
			continue
		}
		var s string
		if json.Unmarshal(m.Content, &s) == nil {
			return s
		}
		var blocks []struct {
			Type    string          `json:"type"`
			Text    string          `json:"text"`
			Content json.RawMessage `json:"content"`
		}
		_ = json.Unmarshal(m.Content, &blocks)
		var parts []string
		for _, b := range blocks {
			if b.Text != "" {
				parts = append(parts, b.Text)
			}
			if len(b.Content) > 0 {
				parts = append(parts, string(b.Content))
			}
		}
		return strings.Join(parts, "\n")
	}
	return ""
}

func sseText(text string) string {
	quoted, _ := json.Marshal(text)
	return "event: message_start\n" +
		`data: {"type":"message_start","message":{"usage":{"input_tokens":5}}}` + "\n\n" +
		"event: content_block_start\n" +
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text"}}` + "\n\n" +
		"event: content_block_delta\n" +
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":` + string(quoted) + `}}` + "\n\n" +
		"event: content_block_stop\n" +
		`data: {"type":"content_block_stop","index":0}` + "\n\n" +
		"event: message_delta\n" +
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":3}}` + "\n\n" +
		"event: message_stop\n" +
		`data: {"type":"message_stop"}` + "\n\n"
}

func sseBash(id, command string) string {
	args, _ := json.Marshal(map[string]string{"command": command})
	partial, _ := json.Marshal(string(args))
	return "event: message_start\n" +
		`data: {"type":"message_start","message":{"usage":{"input_tokens":5}}}` + "\n\n" +
		"event: content_block_start\n" +
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"` + id + `","name":"bash"}}` + "\n\n" +
		"event: content_block_delta\n" +
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":` + string(partial) + `}}` + "\n\n" +
		"event: content_block_stop\n" +
		`data: {"type":"content_block_stop","index":0}` + "\n\n" +
		"event: message_delta\n" +
		`data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":3}}` + "\n\n" +
		"event: message_stop\n" +
		`data: {"type":"message_stop"}` + "\n\n"
}

func (r *sendRig) envValue(key string) string {
	for _, kv := range r.env {
		if k, v, ok := strings.Cut(kv, "="); ok && k == key {
			return v
		}
	}
	return ""
}

func (r *sendRig) mint(t *testing.T, prompt string) string {
	t.Helper()
	born, err := r.cmd("new", "-f", "-j", "--", prompt).Output()
	if err != nil {
		t.Fatalf("new: %v\n%s", err, born)
	}
	lines := strings.Split(strings.TrimSpace(string(born)), "\n")
	var created struct {
		AriaID string `json:"aria_id"`
	}
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &created); err != nil || created.AriaID == "" {
		t.Fatalf("new -j printed no aria id: %q", born)
	}
	return created.AriaID
}

func (r *sendRig) must(t *testing.T, args ...string) {
	t.Helper()
	if out, err := r.cmd(args...).CombinedOutput(); err != nil {
		t.Fatalf("figaro %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

// crash SIGKILLs the daemon, the way a crash or an OOM kill ends it: no drain,
// no shutdown hook.
func (r *sendRig) crash(t *testing.T) {
	t.Helper()
	r.must(t, "stop", "--force")
}

// start brings a daemon back with a command that touches no aria.
func (r *sendRig) start(t *testing.T) {
	t.Helper()
	r.must(t, "ls")
}

func TestAMessageQueuedBehindATurnIsDeliveredAfterTheDaemonStops(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns a daemon and restarts it; skipped under -short")
	}
	prov := newScriptedProvider(t, func(last string) (bool, string) {
		return strings.Contains(last, "first") && !strings.Contains(last, figaro.ResumeNotice), sseText("ack")
	})
	rig := newSendRig(t, prov.srv.URL)

	aria := rig.mint(t, "first")
	prov.awaitAsk(t, 0, 30*time.Second, "first")
	rig.must(t, "send", "-f", "--id", aria, "--", "second")
	deadline := time.Now().Add(15 * time.Second)
	for rig.queued(t, aria) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the second prompt was never queued behind the first turn")
		}
		time.Sleep(50 * time.Millisecond)
	}
	n := prov.count()

	rig.must(t, "stop")
	rig.start(t)

	prov.awaitAsk(t, n, 20*time.Second, "second")
	prov.awaitAsk(t, n, 5*time.Second, figaro.ResumeNotice)
}

func TestAMessageQueuedBehindATurnIsDeliveredAfterTheDaemonCrashes(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns a daemon and kills it; skipped under -short")
	}
	prov := newScriptedProvider(t, func(last string) (bool, string) {
		return strings.Contains(last, "first") && !strings.Contains(last, figaro.ResumeNotice), sseText("ack")
	})
	rig := newSendRig(t, prov.srv.URL)

	aria := rig.mint(t, "first")
	prov.awaitAsk(t, 0, 30*time.Second, "first")
	rig.must(t, "send", "-f", "--id", aria, "--", "second")
	deadline := time.Now().Add(15 * time.Second)
	for rig.queued(t, aria) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the second prompt was never queued behind the first turn")
		}
		time.Sleep(50 * time.Millisecond)
	}
	n := prov.count()

	rig.crash(t)
	rig.start(t)

	prov.awaitAsk(t, n, 20*time.Second, "second")
}

func TestATurnCutByACrashMidToolResumesOnItsOwnAfterRestart(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns a daemon and kills it; skipped under -short")
	}
	started := filepath.Join(t.TempDir(), "tool-started")
	prov := newScriptedProvider(t, func(last string) (bool, string) {
		switch {
		case strings.Contains(last, figaro.ResumeNotice):
			return false, sseText("resumed")
		case strings.Contains(last, "long job"):
			return false, sseBash("toolu_long", fmt.Sprintf("touch %s; sleep 20", started))
		}
		return false, sseText("ack")
	})
	rig := newSendRig(t, prov.srv.URL)

	rig.mint(t, "long job")
	deadline := time.Now().Add(30 * time.Second)
	for {
		if _, err := os.Stat(started); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the tool never started")
		}
		time.Sleep(50 * time.Millisecond)
	}
	n := prov.count()

	rig.crash(t)
	rig.start(t)

	i := prov.awaitAsk(t, n, 20*time.Second, figaro.ResumeNotice)
	if !strings.Contains(prov.ask(i), "process died mid-turn") {
		t.Fatalf("the resumed request does not carry the cut tool's result: %q", prov.ask(i))
	}
	prov.expectQuiet(t, i+1, 2*time.Second, "a resumed turn that finished was resumed again")

	rig.must(t, "stop")
	rig.start(t)
	prov.expectQuiet(t, i+1, 3*time.Second, "a finished turn was resumed by a later restart")
}

func TestATurnCutByAStopResumesOnceAndAResumeCutByACrashIsNotResumedAgain(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns a daemon and restarts it; skipped under -short")
	}
	prov := newScriptedProvider(t, func(last string) (bool, string) {
		return true, ""
	})
	rig := newSendRig(t, prov.srv.URL)

	rig.mint(t, "first")
	prov.awaitAsk(t, 0, 30*time.Second, "first")

	rig.must(t, "stop")
	rig.start(t)
	i := prov.awaitAsk(t, 1, 20*time.Second, figaro.ResumeNotice)

	rig.crash(t)
	rig.start(t)
	prov.expectQuiet(t, i+1, 3*time.Second, "a resume cut by a crash was resumed a second time")
}

func TestATurnTheUserHungUpIsNotResumedAfterRestart(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns a daemon and restarts it; skipped under -short")
	}
	prov := newScriptedProvider(t, func(last string) (bool, string) {
		return strings.Contains(last, "first"), sseText("ack")
	})
	rig := newSendRig(t, prov.srv.URL)

	aria := rig.mint(t, "first")
	prov.awaitAsk(t, 0, 30*time.Second, "first")
	rig.must(t, "hup", aria)
	time.Sleep(500 * time.Millisecond)
	n := prov.count()

	rig.must(t, "stop")
	rig.start(t)
	prov.expectQuiet(t, n, 3*time.Second, "a turn the user hung up was resumed")
}

// stop --hup is the brake on a runaway turn: every running turn is hung up
// before the daemon goes, so the next daemon resumes none of them. What was
// queued is kept and delivered, as a hup keeps it.
func TestStopHupLeavesNoTurnToResume(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns a daemon and restarts it; skipped under -short")
	}
	prov := newScriptedProvider(t, func(last string) (bool, string) {
		return strings.Contains(last, "runaway"), sseText("ack")
	})
	rig := newSendRig(t, prov.srv.URL)

	one := rig.mint(t, "runaway one")
	rig.mint(t, "runaway two")
	prov.awaitAsk(t, 0, 30*time.Second, "runaway one")
	prov.awaitAsk(t, 0, 30*time.Second, "runaway two")
	rig.must(t, "send", "-f", "--id", one, "--", "queued behind it")
	deadline := time.Now().Add(15 * time.Second)
	for rig.queued(t, one) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the prompt was never queued behind the runaway turn")
		}
		time.Sleep(50 * time.Millisecond)
	}
	n := prov.count()

	rig.must(t, "stop", "--hup")
	rig.start(t)

	i := prov.awaitAsk(t, n, 20*time.Second, "queued behind it")
	if strings.Contains(prov.ask(i), figaro.ResumeNotice) {
		t.Fatalf("a turn hung up by stop --hup was resumed with the queue: %q", prov.ask(i))
	}
	prov.expectQuiet(t, i+1, 3*time.Second, "a turn hung up by stop --hup was resumed")
}

// The brake must hold even when the turn it brakes will not die: a tool that
// ignores its cancellation outlives every grace, the daemon is then SIGKILLed,
// and the next start still resumes nothing.
func TestStopHupHoldsWhenATurnWillNotDie(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns a daemon and kills it; skipped under -short")
	}
	started := filepath.Join(t.TempDir(), "tool-started")
	prov := newScriptedProvider(t, func(last string) (bool, string) {
		if strings.Contains(last, "stubborn") {
			return false, sseBash("toolu_stubborn",
				fmt.Sprintf("trap '' INT TERM HUP; touch %s; for i in $(seq 75); do sleep 0.2; done", started))
		}
		return false, sseText("ack")
	})
	rig := newSendRig(t, prov.srv.URL)

	rig.mint(t, "stubborn")
	deadline := time.Now().Add(30 * time.Second)
	for {
		if _, err := os.Stat(started); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the tool never started")
		}
		time.Sleep(50 * time.Millisecond)
	}
	n := prov.count()

	rig.must(t, "stop", "--force", "--hup")
	rig.start(t)
	prov.expectQuiet(t, n, 4*time.Second, "a turn stopped with --hup was resumed")
}
