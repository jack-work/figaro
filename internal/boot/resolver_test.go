package boot

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/jack-work/figaro/internal/config"
)

func resetHush() {
	hushOnce, hushInstance, hushErr = sync.Once{}, nil, nil
	hushUsersMu.Lock()
	hushUsed, hushUsers = nil, nil
	hushUsersMu.Unlock()
}

func serverEnv(t *testing.T) (cfgDir, credDir string) {
	t.Helper()
	resetHush()
	t.Cleanup(resetHush)
	for _, k := range []string{"ANTHROPIC_API_KEY", "ANTHROPIC_OAUTH_TOKEN", "CLAUDE_CODE_OAUTH_TOKEN", "COPILOT_GITHUB_TOKEN", "GH_TOKEN", "GITHUB_TOKEN"} {
		t.Setenv(k, "")
	}
	t.Setenv("FIGARO_HUSH_DIR", t.TempDir())
	cfgDir, credDir = t.TempDir(), t.TempDir()
	t.Setenv("CREDENTIALS_DIRECTORY", credDir)
	if err := os.MkdirAll(filepath.Join(cfgDir, "providers"), 0o700); err != nil {
		t.Fatal(err)
	}
	return cfgDir, credDir
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func assertHushUntouched(t *testing.T) {
	t.Helper()
	if hushInstance != nil || hushUsed != nil {
		t.Fatal("hush was constructed although a file credential answered first")
	}
}

// A server daemon with LoadCredential and no hush must resolve a provider
// that would otherwise fall through to hush's OAuth, without touching hush.
func TestCredentialsDirectoryResolvesWithoutHush(t *testing.T) {
	cfgDir, credDir := serverEnv(t)
	writeFile(t, filepath.Join(credDir, "anthropic"), "sk-from-credentials\n")

	r, err := BuildResolver(&config.Loaded{ConfigDir: cfgDir}, "anthropic")
	if err != nil {
		t.Fatal(err)
	}
	tok, err := r.Resolve()
	if err != nil || tok != "sk-from-credentials" {
		t.Fatalf("Resolve = %q, %v", tok, err)
	}
	if err := r.Invalidate(tok); err != nil {
		t.Fatalf("Invalidate: %v", err)
	}
	assertHushUntouched(t)
}

func TestAPIKeyFileRelativeResolvesUnderCredentialsDirectoryAndWins(t *testing.T) {
	cfgDir, credDir := serverEnv(t)
	writeFile(t, filepath.Join(credDir, "anthropic"), "sk-convention")
	writeFile(t, filepath.Join(credDir, "claude-key"), "sk-configured")
	writeFile(t, filepath.Join(cfgDir, "providers", "anthropic.toml"), `api_key_file = "claude-key"`+"\n")

	r, _ := BuildResolver(&config.Loaded{ConfigDir: cfgDir}, "anthropic")
	tok, err := r.Resolve()
	if err != nil || tok != "sk-configured" {
		t.Fatalf("Resolve = %q, %v: an explicit api_key_file must win over the convention", tok, err)
	}
	assertHushUntouched(t)
}

func TestAPIKeyFileAbsolutePath(t *testing.T) {
	cfgDir, _ := serverEnv(t)
	key := filepath.Join(t.TempDir(), "key")
	writeFile(t, key, "sk-absolute")
	writeFile(t, filepath.Join(cfgDir, "providers", "anthropic.toml"), `api_key_file = "`+key+`"`+"\n")

	r, _ := BuildResolver(&config.Loaded{ConfigDir: cfgDir}, "anthropic")
	if tok, err := r.Resolve(); err != nil || tok != "sk-absolute" {
		t.Fatalf("Resolve = %q, %v", tok, err)
	}
}

// A configured file that is missing is a misconfiguration, not a pass: the
// error must survive to the caller rather than be masked by a later strategy.
func TestAPIKeyFileMissingIsAnError(t *testing.T) {
	cfgDir, _ := serverEnv(t)
	writeFile(t, filepath.Join(cfgDir, "providers", "anthropic.toml"), `api_key_file = "/nonexistent/key"`+"\n")

	r, _ := BuildResolver(&config.Loaded{ConfigDir: cfgDir}, "anthropic")
	_, err := r.Resolve()
	if err == nil || !strings.Contains(err.Error(), "/nonexistent/key") {
		t.Fatalf("err = %v, want one naming the missing file", err)
	}
}

func TestAPIKeyFileRelativeWithoutCredentialsDirectory(t *testing.T) {
	cfgDir, _ := serverEnv(t)
	t.Setenv("CREDENTIALS_DIRECTORY", "")
	writeFile(t, filepath.Join(cfgDir, "providers", "anthropic.toml"), `api_key_file = "claude-key"`+"\n")

	r, _ := BuildResolver(&config.Loaded{ConfigDir: cfgDir}, "anthropic")
	_, err := r.Resolve()
	if err == nil || !strings.Contains(err.Error(), "CREDENTIALS_DIRECTORY") {
		t.Fatalf("err = %v, want one naming $CREDENTIALS_DIRECTORY", err)
	}
}

func TestEnvironmentStillOutranksFiles(t *testing.T) {
	cfgDir, credDir := serverEnv(t)
	writeFile(t, filepath.Join(credDir, "anthropic"), "sk-file")
	t.Setenv("ANTHROPIC_API_KEY", "sk-env")

	r, _ := BuildResolver(&config.Loaded{ConfigDir: cfgDir}, "anthropic")
	if tok, _ := r.Resolve(); tok != "sk-env" {
		t.Fatalf("Resolve = %q, want the environment's", tok)
	}
}

// Building an exchange provider's resolver must not reach hush; only using it may.
func TestExchangeResolverIsLazy(t *testing.T) {
	cfgDir, _ := serverEnv(t)
	r, err := BuildResolver(&config.Loaded{ConfigDir: cfgDir}, "copilot")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := r.(*exchangeResolver); !ok {
		t.Fatalf("copilot resolver = %T, want the exchange", r)
	}
	if err := r.Invalidate("x"); err != nil {
		t.Fatal(err)
	}
	if ep := r.(*exchangeResolver).Endpoint(); ep != "" {
		t.Fatalf("Endpoint = %q before any resolve", ep)
	}
	assertHushUntouched(t)
}
