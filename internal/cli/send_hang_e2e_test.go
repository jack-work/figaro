package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/creack/pty"
)

// A send that lands behind a running turn, with a terminal on stdin and a file
// on stdout, used to join through the pager and wait there for keys nobody
// would press. The failing provider makes the turn end before it streams,
// which is how the hang was first seen.

type heldProvider struct {
	srv      *httptest.Server
	held     chan struct{}
	release  chan struct{}
	holdOnce sync.Once
}

func newHeldProvider(t *testing.T) *heldProvider {
	t.Helper()
	p := &heldProvider{held: make(chan struct{}), release: make(chan struct{})}
	p.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/messages") {
			first := false
			p.holdOnce.Do(func() { first = true })
			if first {
				close(p.held)
				select {
				case <-p.release:
				case <-r.Context().Done():
					return
				}
			}
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"type":"error","error":{"type":"invalid_request_error","message":"held provider refuses"}}`))
	}))
	t.Cleanup(func() {
		select {
		case <-p.release:
		default:
			close(p.release)
		}
		p.srv.Close()
	})
	return p
}

type sendRig struct {
	bin string
	env []string
}

func newSendRig(t *testing.T, baseURL string) *sendRig {
	t.Helper()
	root, err := os.MkdirTemp("/var/tmp", "figaro-sendhang-")
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
	rig := &sendRig{bin: smokeBinary(t), env: append(env,
		"FIGARO_RUNTIME_DIR="+filepath.Join(root, "run"),
		"FIGARO_STATE_DIR="+filepath.Join(root, "state"),
		"FIGARO_CACHE_DIR="+filepath.Join(root, "cache"),
		"FIGARO_CONFIG_DIR="+filepath.Join(root, "config"),
		"FIGARO_HUSH_APP=figaro-test-sendhang",
		"FIGARO_HUSH_DIR="+filepath.Join(root, "hush"),
		"FIGARO_HUSH_PASSPHRASE=figaro-sendhang-e2e",
		"FIGARO_HUSH_TTL=2m",
		"FIGARO_NO_BIND=1",
		"ANTHROPIC_API_KEY=sk-ant-held",
		"ANTHROPIC_BASE_URL="+baseURL,
	)}
	t.Cleanup(func() { _, _ = rig.cmd("stop", "--force").CombinedOutput() })
	return rig
}

func (r *sendRig) cmd(args ...string) *exec.Cmd {
	c := exec.Command(r.bin, args...)
	c.Env = r.env
	return c
}

func (r *sendRig) queued(t *testing.T, aria string) int {
	t.Helper()
	out, err := r.cmd("queue", "--id", aria, "-j").Output()
	if err != nil {
		return 0
	}
	var q struct {
		Queue []json.RawMessage `json:"queue"`
	}
	if json.Unmarshal(bytes.TrimSpace(out), &q) != nil {
		return 0
	}
	return len(q.Queue)
}

func TestSendBehindARunningTurnWithStdoutRedirectedExitsWhenItsTurnFails(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns a daemon and a real send; skipped under -short")
	}
	prov := newHeldProvider(t)
	rig := newSendRig(t, prov.srv.URL)

	born, err := rig.cmd("new", "-j", "--", "x").Output()
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
	select {
	case <-prov.held:
	case <-time.After(30 * time.Second):
		t.Fatal("the first turn never reached the provider")
	}

	ptmx, tty, err := pty.Open()
	if err != nil {
		t.Fatalf("pty: %v", err)
	}
	defer ptmx.Close()
	defer tty.Close()
	out, err := os.Create(filepath.Join(t.TempDir(), "send.out"))
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()

	send := rig.cmd("send", "--id", created.AriaID, "--", "y")
	send.Stdin, send.Stdout, send.Stderr = tty, out, out
	if err := send.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan error, 1)
	go func() { exited <- send.Wait() }()

	deadline := time.Now().Add(15 * time.Second)
	for rig.queued(t, created.AriaID) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the second prompt was never queued behind the first turn")
		}
		time.Sleep(50 * time.Millisecond)
	}
	close(prov.release)

	select {
	case err := <-exited:
		var ee *exec.ExitError
		if !errors.As(err, &ee) || ee.ExitCode() != exitTurnFailed {
			body, _ := os.ReadFile(out.Name())
			t.Fatalf("send exited with %v, want exit %d for a failed turn\n%s", err, exitTurnFailed, tailBytes(body, 600))
		}
	case <-time.After(20 * time.Second):
		_ = send.Process.Kill()
		<-exited
		body, _ := os.ReadFile(out.Name())
		t.Fatalf("send was still waiting 20s after its turn failed\n%s", tailBytes(body, 600))
	}
}

func tailBytes(b []byte, n int) string {
	if len(b) > n {
		b = b[len(b)-n:]
	}
	return strings.ToValidUTF8(string(b), "?")
}
