package boot

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jack-work/figaro/internal/config"
	providerPkg "github.com/jack-work/figaro/internal/provider"
)

func credUnixSocket(t *testing.T, handler http.HandlerFunc) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "bootcred-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "cred.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: handler}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return sock
}

// A provider naming [cred] resolves exclusively from that socket, ignoring
// env, $CREDENTIALS_DIRECTORY, api_key, and embedded hush.
func TestAProviderWithCredSocketResolvesExclusivelyThroughIt(t *testing.T) {
	cfgDir, credDir := serverEnv(t)
	t.Setenv("ANTHROPIC_API_KEY", "sk-stray-env")
	writeFile(t, filepath.Join(credDir, "anthropic"), "sk-stray-dir\n")

	var hits atomic.Int32
	sock := credUnixSocket(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/cred/anthropic-work" {
			http.NotFound(w, r)
			return
		}
		n := hits.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"name":       "anthropic-work",
			"token":      fmt.Sprintf("cred-tok-%d", n),
			"expires_at": time.Now().Add(10 * time.Minute).Unix(),
		})
	})

	writeFile(t, filepath.Join(cfgDir, "providers", "anthropic.toml"), fmt.Sprintf(
		"api_key = \"sk-stray-file\"\n[cred]\nsocket = %q\nname = \"anthropic-work\"\n", sock))
	loaded := &config.Loaded{ConfigDir: cfgDir}

	for i := 0; i < 3; i++ {
		r, err := BuildResolver(loaded, "anthropic")
		if err != nil {
			t.Fatal(err)
		}
		tok, err := r.Resolve()
		if err != nil || tok != "cred-tok-1" {
			t.Fatalf("aria %d: Resolve = %q, %v", i, tok, err)
		}
	}
	if hits.Load() != 1 {
		t.Fatalf("expected 1 shared socket fetch across arias, got %d", hits.Load())
	}

	r, _ := BuildResolver(loaded, "anthropic")
	if err := r.Invalidate("cred-tok-1"); err != nil {
		t.Fatal(err)
	}
	if tok, err := r.Resolve(); err != nil || tok != "cred-tok-2" {
		t.Fatalf("after Invalidate: Resolve = %q, %v", tok, err)
	}
	assertHushUntouched(t)
}

// A missing or refusing cred socket fails cleanly without falling back to
// stray keys or waking embedded hush.
func TestRefusingOrMissingCredSocketNeverFallsBackOrWakesHush(t *testing.T) {
	cfgDir, credDir := serverEnv(t)
	t.Setenv("ANTHROPIC_API_KEY", "sk-stray-env")
	writeFile(t, filepath.Join(credDir, "anthropic"), "sk-stray-dir\n")

	missing := filepath.Join(t.TempDir(), "missing.sock")
	writeFile(t, filepath.Join(cfgDir, "providers", "anthropic.toml"), fmt.Sprintf(
		"api_key = \"sk-stray-file\"\n[cred]\nsocket = %q\nname = \"anthropic-work\"\n", missing))
	loaded := &config.Loaded{ConfigDir: cfgDir}

	r, err := BuildResolver(loaded, "anthropic")
	if err != nil {
		t.Fatal(err)
	}
	if tok, err := r.Resolve(); err == nil || !strings.Contains(err.Error(), missing) {
		t.Fatalf("missing socket: tok = %q, err = %v", tok, err)
	}
	if hushAgentGone(fmt.Errorf("check")) {
		t.Fatal("unexpected hushAgentGone")
	}
	assertHushUntouched(t)

	refusing := credUnixSocket(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":"forbidden","reason":"peer not allowed"}`))
	})
	writeFile(t, filepath.Join(cfgDir, "providers", "copilot.toml"), fmt.Sprintf(
		"[cred]\nsocket = %q\nname = \"copilot-work\"\n", refusing))
	rc, err := BuildResolver(loaded, "copilot")
	if err != nil {
		t.Fatal(err)
	}
	if tok, err := rc.Resolve(); err == nil || !strings.Contains(err.Error(), "peer not allowed") {
		t.Fatalf("forbidden socket: tok = %q, err = %v", tok, err)
	}
	assertHushUntouched(t)
}

// [cred] rejects relative sockets, missing/bad names, and coexistence with
// [oauth] or service_token.
func TestCredConfigValidationAndMutualExclusion(t *testing.T) {
	cases := map[string]string{
		"empty cred table":   "[cred]\n",
		"missing socket":     "[cred]\nname = \"inst\"\n",
		"relative socket":    "[cred]\nsocket = \"run/hush-cred/inst.sock\"\nname = \"inst\"\n",
		"slash socket":       "[cred]\nsocket = \"/run/hush-cred/\"\nname = \"inst\"\n",
		"missing name":       "[cred]\nsocket = \"/run/hush-cred/inst.sock\"\n",
		"slash in name":      "[cred]\nsocket = \"/run/hush-cred/inst.sock\"\nname = \"a/b\"\n",
		"query in name":      "[cred]\nsocket = \"/run/hush-cred/inst.sock\"\nname = \"a?x=1\"\n",
		"dotdot name":        "[cred]\nsocket = \"/run/hush-cred/inst.sock\"\nname = \"..\"\n",
		"with service_token": "service_token = \"rou2\"\n[cred]\nsocket = \"/run/hush-cred/inst.sock\"\nname = \"inst\"\n",
		"with oauth":         "[oauth]\nissuer = \"https://auth.example\"\nclient_id = \"c\"\n[cred]\nsocket = \"/run/hush-cred/inst.sock\"\nname = \"inst\"\n",
	}
	for label, doc := range cases {
		t.Run(label, func(t *testing.T) {
			cfgDir, _ := serverEnv(t)
			writeFile(t, filepath.Join(cfgDir, "providers", "anthropic.toml"), doc)
			loaded := &config.Loaded{ConfigDir: cfgDir}
			if _, err := BuildResolver(loaded, "anthropic"); err == nil {
				t.Fatalf("%s: BuildResolver succeeded unexpectedly", label)
			}
			if _, err := CredentialSources(loaded, providerPkg.Lookup("anthropic"), nil); err == nil {
				t.Fatalf("%s: CredentialSources succeeded unexpectedly", label)
			}
		})
	}
}

// CredentialSources probes [cred] live and reports status or refusal reason
// without ever exposing the bearer token.
func TestCredentialSourcesProbesCredSocketWithoutLeakingToken(t *testing.T) {
	const secret = "sk-ant-oat01-never-print-me"
	cfgDir, _ := serverEnv(t)
	reg := providerPkg.Lookup("anthropic")

	var mode atomic.Int32
	sock := credUnixSocket(t, func(w http.ResponseWriter, r *http.Request) {
		switch mode.Load() {
		case 0:
			_ = json.NewEncoder(w).Encode(map[string]any{
				"name":       "anthropic-work",
				"token":      secret,
				"expires_at": time.Now().Add(time.Hour).Unix(),
			})
		case 1:
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"error":"forbidden","reason":"peer not allowed"}`))
		default:
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":"credential_stale","reason":"sign-in needed"}`))
		}
	})

	writeFile(t, filepath.Join(cfgDir, "providers", "anthropic.toml"), fmt.Sprintf(
		"[cred]\nsocket = %q\nname = \"anthropic-work\"\n", sock))
	loaded := &config.Loaded{ConfigDir: cfgDir}

	if ConsultsHushOAuth(loaded, reg) {
		t.Fatal("ConsultsHushOAuth should be false when [cred] is configured")
	}

	srcs, err := CredentialSources(loaded, reg, []string{"anthropic"})
	if err != nil || len(srcs) != 1 || srcs[0].Kind != SourceCred || srcs[0].Owned {
		t.Fatalf("ready sources = %+v, err = %v", srcs, err)
	}
	if !strings.Contains(srcs[0].Where, sock) || !strings.Contains(srcs[0].Where, "anthropic-work: ready") {
		t.Fatalf("Where = %q", srcs[0].Where)
	}
	if strings.Contains(srcs[0].Where, secret) {
		t.Fatalf("Where leaked token: %q", srcs[0].Where)
	}

	mode.Store(1)
	srcs, err = CredentialSources(loaded, reg, nil)
	if err != nil || !strings.Contains(srcs[0].Where, "peer not allowed") {
		t.Fatalf("forbidden Where = %+v, err = %v", srcs, err)
	}

	mode.Store(2)
	srcs, err = CredentialSources(loaded, reg, nil)
	if err != nil || !strings.Contains(srcs[0].Where, "sign-in needed") {
		t.Fatalf("stale Where = %+v, err = %v", srcs, err)
	}
	assertHushUntouched(t)
}
