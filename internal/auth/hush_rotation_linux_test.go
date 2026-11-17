package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"filippo.io/age"
	"github.com/jack-work/figaro/internal/testreap"
	hush "github.com/jack-work/hush/client"
	"github.com/stretchr/testify/require"
)

type hushReadiness struct {
	mu       sync.Mutex
	output   bytes.Buffer
	once     sync.Once
	stopOnce sync.Once
	ready    chan struct{}
	stopped  chan struct{}
}

func (w *hushReadiness) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n, err := w.output.Write(p)
	if strings.Contains(w.output.String(), "agent started,") {
		w.once.Do(func() { close(w.ready) })
	}
	if strings.Contains(w.output.String(), "agent stopped") {
		w.stopOnce.Do(func() { close(w.stopped) })
	}
	return n, err
}

func (w *hushReadiness) text() string { w.mu.Lock(); defer w.mu.Unlock(); return w.output.String() }

type isolatedHush struct {
	binary, base, certificate string
	identity                  []byte
	process                   *exec.Cmd
	logs                      *hushReadiness
	logPipe                   *os.File
	client                    *hush.Client
}

func (h *isolatedHush) start(t *testing.T) {
	t.Helper()
	reader, writer, err := os.Pipe()
	require.NoError(t, err)
	h.logs = &hushReadiness{ready: make(chan struct{}), stopped: make(chan struct{})}
	logPath := filepath.Join(h.base, "state", "hush.log")
	if err := syscall.Mkfifo(logPath, 0600); err != nil && !os.IsExist(err) {
		t.Fatal("create isolated hush log pipe")
	}
	h.logPipe, err = os.OpenFile(logPath, os.O_RDWR, 0600)
	require.NoError(t, err)
	go io.Copy(h.logs, h.logPipe)
	h.process = exec.Command(h.binary, "up", "--ttl", "5m")
	h.process.Env = []string{
		"HOME=" + h.base,
		"HUSH_CONFIG_DIR=" + filepath.Join(h.base, "config"),
		"HUSH_STATE_DIR=" + filepath.Join(h.base, "state"),
		"HUSH_RUNTIME_DIR=" + filepath.Join(h.base, "run"),
		"HUSH_KEYRING_SERVICE=figaro-isolated-auth-test",
		"HUSH_AGENT_CHILD=1",
		"SSL_CERT_FILE=" + h.certificate,
		testreap.Env(),
	}
	h.process.ExtraFiles = []*os.File{reader}
	h.process.Stdout = io.Discard
	h.process.Stderr = h.logs
	require.NoError(t, h.process.Start())
	reader.Close()
	_, err = writer.Write(h.identity)
	require.NoError(t, err)
	writer.Close()
	select {
	case <-h.logs.ready:
	case <-time.After(10 * time.Second):
		h.process.Process.Kill()
		h.process.Wait()
		t.Fatal("isolated hush failed to become ready")
	}
	require.NoError(t, h.client.Ping())
}

func (h *isolatedHush) stop(t *testing.T) {
	t.Helper()
	if h.process == nil {
		return
	}
	process := h.process
	h.process = nil
	defer h.logPipe.Close()
	require.NoError(t, process.Process.Signal(os.Interrupt))
	done := make(chan error, 1)
	go func() { done <- process.Wait() }()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		process.Process.Kill()
		<-done
		t.Fatal("isolated hush did not stop")
	}
	select {
	case <-h.logs.stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("isolated hush log did not drain")
	}
	require.NotContains(t, h.logs.text(), "fake-refresh-rotation-")
	require.NotContains(t, h.logs.text(), "fake-access-rotation-")
}

func TestIsolatedHushRotatesAndPersistsDeclaredPKCEGrant(t *testing.T) {
	binary := os.Getenv("FIGARO_AUTH_TEST_HUSH")
	if binary == "" {
		t.Skip("run scripts/subscription-oauth-hush.sh for the actual hush0.14.6 process fixture")
	}
	p := newPKCEIdP(t)
	var mu sync.Mutex
	expected := "rt"
	rotations := 0
	reject := false
	concurrent := false
	original := p.srv.Config.Handler
	p.srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, r.ParseForm())
		if r.PostForm.Get("grant_type") != "refresh_token" {
			original.ServeHTTP(w, r)
			return
		}
		mu.Lock()
		overlapping := concurrent
		mu.Unlock()
		if overlapping {
			select {
			case <-time.After(200 * time.Millisecond):
			case <-r.Context().Done():
				return
			}
		}
		mu.Lock()
		defer mu.Unlock()
		if reject {
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]string{"error": "invalid_grant", "error_description": "denied " + r.PostForm.Get("refresh_token") + " private-issuer-detail"})
			return
		}
		if r.PostForm.Get("refresh_token") != expected {
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]string{"error": "invalid_grant"})
			return
		}
		rotations++
		expected = fmt.Sprintf("fake-refresh-rotation-%d", rotations)
		json.NewEncoder(w).Encode(map[string]any{"access_token": fmt.Sprintf("fake-access-rotation-%d", rotations), "refresh_token": expected, "expires_in": 3600})
	})
	cfg := p.config(t)
	cfg.RedirectURI = availableCallback(t, "127.0.0.1")
	tokens, err := loginPKCE(context.Background(), cfg, p.browser(t, codeFor), 3*time.Second, quiet{})
	require.NoError(t, err)
	base, err := os.MkdirTemp("/var/tmp", "figaro-auth-hush-")
	require.NoError(t, err)
	t.Cleanup(func() { os.RemoveAll(base) })
	for _, sub := range []string{"config", "state", "run"} {
		require.NoError(t, os.MkdirAll(filepath.Join(base, sub), 0700))
	}
	key, err := age.GenerateX25519Identity()
	require.NoError(t, err)
	identity := []byte(key.String() + "\n")
	defer clear(identity)
	certificate := filepath.Join(base, "issuer.crt")
	require.NoError(t, os.WriteFile(certificate, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: p.srv.Certificate().Raw}), 0600))
	h := &isolatedHush{binary: binary, base: base, certificate: certificate, identity: identity, client: hush.NewWithSocket(filepath.Join(base, "run", "agent.sock"))}
	h.start(t)
	defer h.stop(t)
	require.NoError(t, h.client.OAuthRegister(hush.OAuthRegisterRequest{Name: "declared", TokenURL: cfg.TokenURL, ClientID: cfg.ClientID, Scopes: cfg.Scope, AccessToken: tokens.AccessToken, RefreshToken: tokens.RefreshToken, ExpiresIn: tokens.ExpiresIn, Grant: hush.GrantRefreshToken}))
	resolver := &OAuth{Name: "declared", Hush: h.client}
	token, err := resolver.Resolve()
	require.NoError(t, err)
	require.Equal(t, tokens.AccessToken, token)
	var apiCalls atomic.Int32
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call := apiCalls.Add(1)
		if call%2 == 1 {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		want := fmt.Sprintf("Bearer fake-access-rotation-%d", call/2)
		if r.Header.Get("Authorization") != want {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer api.Close()
	for i := 1; i <= 2; i++ {
		resp, err := SendRenewing(resolver, func(credential string) (*http.Response, error) {
			req, err := http.NewRequest(http.MethodPost, api.URL, nil)
			if err != nil {
				return nil, err
			}
			req.Header.Set("Authorization", "Bearer "+credential)
			return api.Client().Do(req)
		})
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, resp.StatusCode)
		resp.Body.Close()
		token, err = resolver.Resolve()
		require.NoError(t, err)
		require.Equal(t, fmt.Sprintf("fake-access-rotation-%d", i), token)
	}
	require.EqualValues(t, 4, apiCalls.Load())
	h.stop(t)
	h.start(t)
	token, err = resolver.Resolve()
	require.NoError(t, err)
	require.Equal(t, "fake-access-rotation-2", token)
	require.NoError(t, resolver.Invalidate(token))
	token, err = resolver.Resolve()
	require.NoError(t, err)
	require.Equal(t, "fake-access-rotation-3", token)
	mu.Lock()
	require.Equal(t, 3, rotations)
	concurrent = true
	mu.Unlock()
	var group sync.WaitGroup
	start := make(chan struct{})
	errs := make(chan error, 16)
	for i := 0; i < 16; i++ {
		group.Add(1)
		go func() { defer group.Done(); <-start; errs <- resolver.Invalidate(token) }()
	}
	close(start)
	group.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	token, err = resolver.Resolve()
	require.NoError(t, err)
	require.Equal(t, "fake-access-rotation-4", token)
	mu.Lock()
	require.Equal(t, 4, rotations, "concurrent 401 recovery must join one refresh")
	concurrent = false
	reject = true
	mu.Unlock()
	err = resolver.Invalidate(token)
	require.ErrorIs(t, err, ErrLoginRequired)
	require.NotContains(t, err.Error(), "fake-refresh")
	require.NotContains(t, err.Error(), "private-issuer-detail")
	require.ErrorContains(t, err, "figaro provider login declared")
	_, err = resolver.Resolve()
	require.ErrorIs(t, err, ErrLoginRequired)
	h.stop(t)
	logs := h.logs.text()
	require.NotContains(t, logs, "fake-refresh-rotation-")
	require.NotContains(t, logs, "fake-access-rotation-")
}
