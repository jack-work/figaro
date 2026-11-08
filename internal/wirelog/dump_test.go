package wirelog

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func dumped(t *testing.T, dir string) string {
	t.Helper()
	var all strings.Builder
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		raw, err := os.ReadFile(p)
		all.Write(raw)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return all.String()
}

// A wire dump is a debugging aid people paste and keep, and `figaro set
// system.environment.figaro_wire_dir` turns it on from any shell. It wrote
// the bearer token and the API key in the clear, against the rule that
// secrets never reach disk unsealed. The dump keeps every header's name and
// an Authorization scheme, and drops every credential value.
func TestWireDumpNeverWritesACredential(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Set-Cookie", "session=cookie-secret")
		w.Header().Set("X-Request-Id", "req-1")
		io.WriteString(w, "ok")
	}))
	defer srv.Close()

	dir := t.TempDir()
	ctx := WithLogging(context.Background(), "94f0752b", dir)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, srv.URL+"/v1/messages", strings.NewReader(`{"model":"m"}`))
	req.Header.Set("Authorization", "Bearer bearer-secret")
	req.Header.Set("X-Api-Key", "apikey-secret")
	req.Header.Set("Api-Key", "azure-secret")
	req.Header.Set("Cookie", "c=cookie-secret-2")
	req.Header.Set("Proxy-Authorization", "Basic proxy-secret")
	req.Header.Set("X-Goog-Api-Key", "goog-secret")
	req.Header.Set("Anthropic-Version", "2023-06-01")
	resp, err := newClient().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	io.ReadAll(resp.Body)
	resp.Body.Close()

	got := dumped(t, dir)
	for _, secret := range []string{"bearer-secret", "apikey-secret", "azure-secret", "cookie-secret", "proxy-secret", "goog-secret"} {
		if strings.Contains(got, secret) {
			t.Errorf("the dump holds %q:\n%s", secret, got)
		}
	}
	for _, kept := range []string{"Authorization: Bearer [redacted]", "Proxy-Authorization: Basic [redacted]", "X-Api-Key: [redacted]", "Set-Cookie: [redacted]", "Anthropic-Version: 2023-06-01", "X-Request-Id: req-1", `{"model":"m"}`, "ok"} {
		if !strings.Contains(got, kept) {
			t.Errorf("the dump lost %q:\n%s", kept, got)
		}
	}
}

// The dump's only guard was the per-aria directory's 0700, which MkdirAll
// leaves alone on a directory that already exists, and the files came out
// 0644 under the usual umask. A dump is the conversation in full: its files
// are 0600 and its directory is held at 0700 even when someone loosened it.
func TestWireDumpIsPrivateToTheOwner(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "ok") }))
	defer srv.Close()

	dir := t.TempDir()
	loose := filepath.Join(dir, "94f0752b")
	if err := os.Mkdir(loose, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(loose, 0o755); err != nil {
		t.Fatal(err)
	}
	ctx := WithLogging(context.Background(), "94f0752b", dir)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, srv.URL, strings.NewReader("{}"))
	resp, err := newClient().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	io.ReadAll(resp.Body)
	resp.Body.Close()

	st, err := os.Stat(loose)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o700 {
		t.Errorf("aria dir is %o, want 700", st.Mode().Perm())
	}
	files, _ := filepath.Glob(filepath.Join(loose, "*"))
	if len(files) != 2 {
		t.Fatalf("want a request and a response file, got %v", files)
	}
	for _, f := range files {
		st, err := os.Stat(f)
		if err != nil {
			t.Fatal(err)
		}
		if st.Mode().Perm() != 0o600 {
			t.Errorf("%s is %o, want 600", filepath.Base(f), st.Mode().Perm())
		}
	}
}

// No provider puts a key in the query today, but one that did (?key=, as
// Google's APIs take) would have landed in clear in the dump's request line
// and in the round-trip log. Query parameters named like a credential are
// masked by the same rule as headers; the rest stay readable.
func TestACredentialInTheQueryIsMaskedInTheDumpAndTheLog(t *testing.T) {
	buf := captureLog(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "ok") }))
	defer srv.Close()

	dir := t.TempDir()
	ctx := WithLogging(context.Background(), "94f0752b", dir)
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/v1/models?key=query-secret&access_token=query-secret-2&alt=sse", nil)
	resp, err := newClient().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	io.ReadAll(resp.Body)
	resp.Body.Close()

	got := dumped(t, dir) + buf.String()
	if strings.Contains(got, "query-secret") {
		t.Errorf("a query credential leaked:\n%s", got)
	}
	if !strings.Contains(got, "alt=sse") || !strings.Contains(got, "key=%5Bredacted%5D") {
		t.Errorf("the query lost its shape:\n%s", got)
	}
}
