package auth

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	hush "github.com/jack-work/hush/client"
)

// scriptedHush answers oauth_get with whatever token it currently holds and
// oauth_refresh with a permanent rejection, counting refreshes.
type scriptedHush struct {
	mu        sync.Mutex
	token     string
	refreshes int
}

func (s *scriptedHush) serve(t *testing.T) *hush.Client {
	t.Helper()
	sock := filepath.Join(t.TempDir(), "hush.sock")
	ln, err := net.Listen("unix", sock)
	require.NoError(t, err)
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			var req struct {
				Op string `json:"op"`
			}
			_ = json.NewDecoder(conn).Decode(&req)
			s.mu.Lock()
			var resp string
			switch req.Op {
			case "oauth_refresh":
				s.refreshes++
				resp = `{"ok":false,"error":"invalid_grant","error_code":"oauth_refresh_permanent"}`
			default:
				b, _ := json.Marshal(map[string]any{"ok": true, "token": s.token})
				resp = string(b)
			}
			s.mu.Unlock()
			_, _ = conn.Write([]byte(resp))
			conn.Close()
		}
	}()
	return hush.NewWithSocket(sock)
}

func (s *scriptedHush) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.refreshes
}

func captureWarnings(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

// TestOAuthStopsAfterPermanentRefresh is the 10-31 storm from figaro's side:
// once hush says the grant is dead, every later request must stop at the
// resolver instead of sending the dead token upstream and asking hush to
// refresh again, and the human is told once what to run.
func TestOAuthStopsAfterPermanentRefresh(t *testing.T) {
	logs := captureWarnings(t)
	agent := &scriptedHush{token: "dead"}
	o := &OAuth{Hush: agent.serve(t), Name: "rou2"}

	err := o.Invalidate("dead")
	require.ErrorIs(t, err, hush.ErrOAuthRefreshPermanent)

	for i := 0; i < 20; i++ {
		_, rerr := o.Resolve()
		require.Error(t, rerr, "request %d resolved the dead token again", i)
		assert.ErrorIs(t, rerr, ErrLoginRequired)
		assert.Contains(t, rerr.Error(), "figaro provider login rou2")
		assert.ErrorIs(t, o.Invalidate("dead"), ErrLoginRequired)
	}
	assert.Equal(t, 1, agent.count(), "hush was asked to refresh a grant it already called dead")
	assert.Equal(t, 1, strings.Count(logs.String(), "needs login"), "the login hint must be said once:\n%s", logs)

	agent.mu.Lock()
	agent.token = "fresh"
	agent.mu.Unlock()

	tok, err := o.Resolve()
	require.NoError(t, err, "a new login must clear the latch")
	assert.Equal(t, "fresh", tok)
	_ = o.Invalidate("fresh")
	assert.Equal(t, 2, agent.count(), "a new token is refreshed normally")
}

// Fixes #64: a refusal the IdP will never take back reaches the caller as one
// actionable line instead of the provider's raw 401, and is not retried.
func TestAPermanentRefusalSurfacesAsLoginRequiredNotA401(t *testing.T) {
	captureWarnings(t)
	agent := &scriptedHush{token: "dead"}
	o := &OAuth{Hush: agent.serve(t), Name: "band"}
	sends := 0
	resp, err := SendRenewing(o, func(string) (*http.Response, error) {
		sends++
		return &http.Response{StatusCode: http.StatusUnauthorized, Body: io.NopCloser(strings.NewReader("revoked"))}, nil
	})
	require.Nil(t, resp, "the raw 401 came back instead of the refusal")
	require.ErrorIs(t, err, ErrLoginRequired)
	assert.Equal(t, "band: sign-in expired, run: figaro provider login band", err.Error())
	assert.Equal(t, 1, sends)

	_, err = SendRenewing(o, func(string) (*http.Response, error) {
		t.Fatal("a dead grant was sent upstream")
		return nil, nil
	})
	assert.Equal(t, "band: sign-in expired, run: figaro provider login band", err.Error())
}

// The refusal's text comes from the IdP and could echo request fields; the
// log keeps only hush's error code.
func TestTheLoginWarningLogsTheClassNotTheIdPText(t *testing.T) {
	logs := captureWarnings(t)
	agent := &scriptedHush{token: "dead"}
	o := &OAuth{Hush: agent.serve(t), Name: "band"}
	_ = o.Invalidate("dead")
	if strings.Contains(logs.String(), "invalid_grant") {
		t.Fatalf("the IdP's refusal text reached the log:\n%s", logs)
	}
	assert.Contains(t, logs.String(), "class=oauth_refresh_permanent")
}
