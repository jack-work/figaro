package angelus_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"text/template"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/internal/angelus"
	"github.com/jack-work/figaro/internal/arialog"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/provider"
	_ "github.com/jack-work/figaro/internal/provider/routed"
	"github.com/jack-work/figaro/sdk"
)

const replayForkNotice = "You are a branch of another figaro."

type wireTap struct {
	mu     sync.Mutex
	bodies map[string][]string
	reply  func(id string, body string) string
}

func (w *wireTap) last(id string) string {
	w.mu.Lock()
	defer w.mu.Unlock()
	b := w.bodies[id]
	if len(b) == 0 {
		return ""
	}
	return b[len(b)-1]
}

func (w *wireTap) count(id string) int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.bodies[id])
}

func chatText(text string) string {
	return fmt.Sprintf("data: {\"id\":\"1\",\"choices\":[{\"delta\":{\"content\":%q}}]}\n\n", text) +
		"data: {\"id\":\"1\",\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":2}}\n\n" +
		"data: [DONE]\n\n"
}

func gatewayDaemon(t *testing.T, tap *wireTap) (*angelus.Angelus, *sdk.Angelus) {
	t.Helper()
	t.Setenv(config.DetachedEnv, "")
	tap.bodies = map[string][]string{}
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		id := r.Header.Get(provider.FigaroIDHeader)
		tap.mu.Lock()
		tap.bodies[id] = append(tap.bodies[id], string(body))
		tap.mu.Unlock()
		if r.URL.Path != "/v1/chat/completions" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		out := chatText("ok")
		if tap.reply != nil {
			out = tap.reply(id, string(body))
		}
		_, _ = io.WriteString(w, out)
	}))
	t.Cleanup(gw.Close)

	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "outfits"), 0o700))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "providers"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.toml"), []byte("default_outfit = \"gw\"\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "providers", "gateway.toml"), []byte(fmt.Sprintf(`
base_url = "%s/v1"
api_key = "test-gateway-token"
send_figaro_id = true
[routing]
default = "chat"
`, gw.URL)), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "outfits", "gw.toml"), []byte(fmt.Sprintf(`
[system]
provider = "gateway"
model = "llama-4"
fork_incantation = %q
`, replayForkNotice)), 0o600))

	a, acli, _, _ := daemonWithProvider(t, dir, nil, nil, func(sc *angelus.ServerConfig) {
		sc.ProviderFactory = func(provName string, knobs provider.Knobs) (provider.Provider, error) {
			reg := provider.Lookup(provName)
			if reg == nil {
				return nil, fmt.Errorf("unknown provider %q", provName)
			}
			return reg.Build(provider.BuildContext{
				Loaded:    sc.Config,
				Knobs:     knobs,
				Templates: template.New("t"),
				OpenTranslator: func(ariaID, ns string) (arialog.Log[[]json.RawMessage], error) {
					return sc.Angelus.Backend.OpenTranslator(ariaID, ns)
				},
			})
		}
	})
	return a, acli
}

func gatewayAria(t *testing.T, a *angelus.Angelus, acli *sdk.Angelus) string {
	t.Helper()
	created, err := acli.Create(t.Context(), dress(t, "gw"), nil)
	require.NoError(t, err)
	askAt(t, a, created.FigaroID, "first question", "Thursday, November 12, 2026, 9AM EST")
	askAt(t, a, created.FigaroID, "second question", "Thursday, November 12, 2026, 2PM EST")
	askAt(t, a, created.FigaroID, "third question", "Monday, November 16, 2026, 8PM EST")
	return created.FigaroID
}

func awaitWire(t *testing.T, a *angelus.Angelus, tap *wireTap, id string, n int) string {
	t.Helper()
	require.Eventually(t, func() bool { return tap.count(id) >= n }, 10*time.Second, 20*time.Millisecond, "%s never reached the provider", id)
	waitIdle(t, a, id)
	return tap.last(id)
}

func TestAPlainForkTellsTheModelItIsABranch(t *testing.T) {
	tap := &wireTap{}
	a, acli := gatewayDaemon(t, tap)
	parent := gatewayAria(t, a, acli)

	resp, err := acli.Fork(t.Context(), parent, 2, 0, nil, nil)
	require.NoError(t, err)
	askAt(t, a, resp.Alternative, "second question", "Monday, November 16, 2026, 9PM EST")
	body := awaitWire(t, a, tap, resp.Alternative, 1)

	require.Contains(t, body, replayForkNotice, "the probe must see the fork notice a plain fork sends")
	require.Contains(t, body, resp.Alternative, "the probe must see a plain fork's own id")
}

// Defends #114: what the provider is sent for a replayed turn carries no
// fork notice, no branch id and no later clock than the original turn's.
func TestTheReplayedTurnReadsAsTheOriginal(t *testing.T) {
	tap := &wireTap{}
	a, acli := gatewayDaemon(t, tap)
	parent := gatewayAria(t, a, acli)
	sent := tap.count(parent)

	resp, err := acli.Replay(t.Context(), parent, 2, nil, nil)
	require.NoError(t, err)
	branch := resp.Alternative
	body := awaitWire(t, a, tap, branch, 1)

	require.NotContains(t, body, branch)
	require.NotContains(t, body, replayForkNotice)
	require.NotContains(t, body, `name=\"fork\"`)
	require.NotContains(t, body, "forked_from")
	require.NotContains(t, body, "third question")
	require.NotContains(t, body, "November 16")
	require.Contains(t, body, "second question")
	require.Contains(t, body, "2PM EST")
	require.Contains(t, body, parent, "the model still reads its own original id")
	require.Equal(t, sent, tap.count(parent), "the replay is not sent as the original")
}

func TestAReplayKeepsItsRealIdOffTheWire(t *testing.T) {
	tap := &wireTap{}
	a, acli := gatewayDaemon(t, tap)
	parent := gatewayAria(t, a, acli)

	resp, err := acli.Replay(t.Context(), parent, 2, nil, nil)
	require.NoError(t, err)
	awaitWire(t, a, tap, resp.Alternative, 1)

	listed, err := acli.List(t.Context())
	require.NoError(t, err)
	var ids []string
	for _, f := range listed.Figaros {
		ids = append(ids, f.ID)
	}
	require.Contains(t, ids, resp.Alternative, "ls shows the branch under its own id")
	form, err := acli.Form(t.Context(), resp.Alternative)
	require.NoError(t, err)
	require.Equal(t, resp.Alternative, *form.Snapshot.Lookup("aria_id"), "the store keeps the branch's real id")
}
