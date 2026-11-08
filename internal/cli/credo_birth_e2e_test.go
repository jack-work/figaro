package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// Gluck: a figaro's credo is fixed once it has spoken. A set of system.credo
// on a live figaro rewrote the system block and threw away its prompt cache;
// the way to a different credo is a new figaro (fork -S, succeed).

type systemRecorder struct {
	srv     *httptest.Server
	mu      sync.Mutex
	systems []string
}

func newSystemRecorder(t *testing.T) *systemRecorder {
	t.Helper()
	p := &systemRecorder{}
	p.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/messages") {
			http.NotFound(w, r)
			return
		}
		body, _ := io.ReadAll(r.Body)
		var req struct {
			System []struct {
				Text string `json:"text"`
			} `json:"system"`
		}
		_ = json.Unmarshal(body, &req)
		var parts []string
		for _, b := range req.System {
			parts = append(parts, b.Text)
		}
		p.mu.Lock()
		p.systems = append(p.systems, strings.Join(parts, "\n"))
		p.mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, sseText("ack"))
	}))
	t.Cleanup(p.srv.Close)
	return p
}

func (p *systemRecorder) lastSystem() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.systems) == 0 {
		return ""
	}
	return p.systems[len(p.systems)-1]
}

func credoAria(t *testing.T, rig *sendRig) string {
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

func TestTheCredoIsFixedOnceTheFigaroHasSpoken(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns a daemon; skipped under -short")
	}
	prov := newSystemRecorder(t)
	rig := newSendRig(t, prov.srv.URL)
	t.Cleanup(func() { _ = rig.cmd("stop").Run() })
	aria := credoAria(t, rig)

	rig.must(t, "set", "--id", aria, "system.credo", "credo-A")
	rig.must(t, "send", "-r", "--id", aria, "--", "hello")
	if got := prov.lastSystem(); !strings.Contains(got, "credo-A") {
		t.Fatalf("a credo set before the first prompt is the system block: %q", got)
	}

	out, err := rig.cmd("set", "--id", aria, "system.credo", "credo-B").CombinedOutput()
	if err == nil {
		t.Fatalf("a set of system.credo on a figaro that has spoken was accepted: %s", out)
	}
	if !strings.Contains(string(out), "fixed at birth") || !strings.Contains(string(out), "fork") {
		t.Fatalf("the refusal names the way out: %s", out)
	}
	rig.must(t, "set", "--id", aria, "system.credo", "credo-A")

	out, err = rig.cmd("send", "-r", "--id", aria, "-S", "system.credo=credo-B", "--", "again").CombinedOutput()
	if err == nil || !strings.Contains(string(out), "fixed at birth") {
		t.Fatalf("a prompt carrying a new credo was accepted: %v %s", err, out)
	}
	rig.must(t, "send", "-r", "--id", aria, "--", "still A")
	if got := prov.lastSystem(); !strings.Contains(got, "credo-A") || strings.Contains(got, "credo-B") {
		t.Fatalf("the credo moved under a live figaro: %q", got)
	}

	forked, err := rig.cmd("fork", aria, "--stay", "-j", "-S", "system.credo=credo-B").Output()
	if err != nil {
		t.Fatalf("a fork's birth patch sets the credo: %v %s", err, forked)
	}
	var branch struct {
		Alternative string `json:"alternative"`
	}
	lines := strings.Split(strings.TrimSpace(string(forked)), "\n")
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &branch); err != nil || branch.Alternative == "" {
		t.Fatalf("fork -j printed no branch: %s", forked)
	}
	rig.must(t, "send", "-r", "--id", branch.Alternative, "--", "who are you")
	if got := prov.lastSystem(); !strings.Contains(got, "credo-B") {
		t.Fatalf("the branch speaks with the credo it was born with: %q", got)
	}
}
