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
