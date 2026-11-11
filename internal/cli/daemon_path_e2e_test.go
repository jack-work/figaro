package cli

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
)

// A daemon autospawned by a client whose PATH is thin handed that PATH to
// every tool shell, and the next daemon it spawned inherited it again. The
// daemon takes its PATH from config before the spawner's.

type pathProvider struct {
	srv    *httptest.Server
	mu     sync.Mutex
	bodies []string
}

func newPathProvider(t *testing.T) *pathProvider {
	t.Helper()
	p := &pathProvider{}
	p.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/messages") {
			http.NotFound(w, r)
			return
		}
		body, _ := io.ReadAll(r.Body)
		p.mu.Lock()
		p.bodies = append(p.bodies, string(body))
		p.mu.Unlock()
		sse := sseText("done")
		if strings.Contains(lastUserText(body), "show the path") {
			sse = sseBash("toolu_path", `echo "TOOLPATH[$PATH]"`)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, sse)
	}))
	t.Cleanup(p.srv.Close)
	return p
}

var toolPathRe = regexp.MustCompile(`TOOLPATH\[([^\]]*)\]`)

func (p *pathProvider) toolPath(t *testing.T) string {
	t.Helper()
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, b := range p.bodies {
		for _, m := range toolPathRe.FindAllStringSubmatch(b, -1) {
			if !strings.Contains(m[1], "$PATH") {
				return m[1]
			}
		}
	}
	t.Fatalf("no tool result carried the PATH; %d requests", len(p.bodies))
	return ""
}

func thinPathRig(t *testing.T, providerURL, angelusTable string) *sendRig {
	t.Helper()
	rig := newSendRig(t, providerURL)
	var configDir string
	for i, kv := range rig.env {
		if v, ok := strings.CutPrefix(kv, "FIGARO_CONFIG_DIR="); ok {
			configDir = v
		}
		if strings.HasPrefix(kv, "PATH=") {
			rig.env[i] = "PATH=/usr/bin:/bin"
		}
	}
	if angelusTable != "" {
		f, err := os.OpenFile(filepath.Join(configDir, "config.toml"), os.O_APPEND|os.O_WRONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(f, angelusTable); err != nil {
			t.Fatal(err)
		}
		f.Close()
	}
	t.Cleanup(func() { _ = rig.cmd("stop").Run() })
	return rig
}

func TestADaemonSpawnedWithAThinPathGivesToolsTheConfiguredPath(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns a daemon; skipped under -short")
	}
	extra := t.TempDir()
	prov := newPathProvider(t)
	rig := thinPathRig(t, prov.srv.URL, "\n[angelus]\npath = [\""+extra+"\", \"/usr/bin\", \"/bin\"]\n")
	turnDoneReason(t, rig, freshAria(t, rig), "show the path")
	if got, want := prov.toolPath(t), extra+":/usr/bin:/bin"; got != want {
		t.Fatalf("tool PATH = %q, want the configured %q", got, want)
	}
}

func TestADevDaemonWithoutConfigKeepsItsSpawnersPath(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns a daemon; skipped under -short")
	}
	prov := newPathProvider(t)
	rig := thinPathRig(t, prov.srv.URL, "")
	turnDoneReason(t, rig, freshAria(t, rig), "show the path")
	if got := prov.toolPath(t); got != "/usr/bin:/bin" {
		t.Fatalf("a daemon on its own runtime dir took PATH %q, want the spawner's /usr/bin:/bin", got)
	}
}
