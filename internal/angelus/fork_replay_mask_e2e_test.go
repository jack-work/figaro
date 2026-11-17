package angelus_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"text/template"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/angelus"
	"github.com/jack-work/figaro/internal/arialog"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/provider"
	_ "github.com/jack-work/figaro/internal/provider/routed"
	"github.com/jack-work/figaro/internal/store"
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

// Defends #114: each mask is the owner's to turn off. With the clock and
// identity masks off the replayed turn reads today and its own id, while
// the fork mark stays hidden and the tools stay stubbed.
func TestTheOwnerTurnsReplayMasksOffOneByOne(t *testing.T) {
	tap := &wireTap{}
	a, acli := gatewayDaemon(t, tap)
	parent := gatewayAria(t, a, acli)

	resp, err := acli.Replay(t.Context(), parent, 2, nil, rawPatch(map[string]string{
		"system.replay.clock":    "false",
		"system.replay.identity": "false",
	}))
	require.NoError(t, err)
	branch := resp.Alternative
	body := sinceLastAnswer(t, awaitWire(t, a, tap, branch, 1))
	require.Contains(t, body, "second question")
	require.Contains(t, body, branch, "identity off: the model reads its own id")
	require.NotContains(t, body, "November 12", "clock off: the model reads the live datetime")
	require.NotContains(t, body, replayForkNotice, "the notice mask is still on")

	marks := replayMarks(t, acli, branch)
	m := marks[len(marks)-1]
	require.NotNil(t, m)
	require.False(t, m.Clock)
	require.False(t, m.Identity)
	require.True(t, m.Notice)
	require.True(t, m.Tools)
	require.Empty(t, m.AriaID)
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

func postNews(t *testing.T, a *angelus.Angelus, other *sdk.Aria, id, news string) {
	t.Helper()
	_, err := other.Set(t.Context(), *rawPatch(map[string]string{"news": fmt.Sprintf("%q", news)}), 0)
	require.NoError(t, err)
	lib, err := a.Backend.(*store.XwalBackend).Libretto(id)
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		v := lib.State().Lookup("news")
		return v != nil && *v == news
	}, 5*time.Second, 5*time.Millisecond, "the libretto never folded %q", news)
}

func TestAReplayReadsStudiedFormsAsTheyStoodAtTheTurn(t *testing.T) {
	tap := &wireTap{}
	a, acli := gatewayDaemon(t, tap)
	created, err := acli.Create(t.Context(), dress(t, "gw"), nil)
	require.NoError(t, err)
	subject := created.FigaroID
	studied, err := acli.Create(t.Context(), dress(t, "gw"), nil)
	require.NoError(t, err)
	other, err := sdk.DialAria(ariaAddr(a, studied.FigaroID), nil)
	require.NoError(t, err)
	defer other.Close()
	me, err := sdk.DialAria(ariaAddr(a, subject), nil)
	require.NoError(t, err)
	defer me.Close()
	_, err = me.Study(t.Context(), studied.FigaroID)
	require.NoError(t, err)

	postNews(t, a, other, studied.FigaroID, "rehearsal booked")
	askAt(t, a, subject, "first question", "Thursday, November 12, 2026, 9AM EST")
	postNews(t, a, other, studied.FigaroID, "curtain at eight")
	askAt(t, a, subject, "second question", "Thursday, November 12, 2026, 2PM EST")
	require.Contains(t, tap.last(subject), "curtain at eight", "the probe must see a study reminder")
	postNews(t, a, other, studied.FigaroID, "the run is over")

	resp, err := acli.Replay(t.Context(), subject, 2, nil, nil)
	require.NoError(t, err)
	body := awaitWire(t, a, tap, resp.Alternative, 1)
	require.Contains(t, body, "curtain at eight")
	require.NotContains(t, body, "the run is over")
}

func chatToolCalls(calls ...[2]string) string {
	var out strings.Builder
	for i, c := range calls {
		args, _ := json.Marshal(c[1])
		fmt.Fprintf(&out, "data: {\"id\":\"1\",\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":%d,\"id\":\"call_%d\",\"type\":\"function\",\"function\":{\"name\":%q,\"arguments\":%s}}]}}]}\n\n", i, i, c[0], args)
	}
	out.WriteString("data: {\"id\":\"1\",\"choices\":[{\"delta\":{},\"finish_reason\":\"tool_calls\"}],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":2}}\n\n")
	out.WriteString("data: [DONE]\n\n")
	return out.String()
}

func lastRole(body string) string {
	var req struct {
		Messages []struct {
			Role string `json:"role"`
		} `json:"messages"`
	}
	if json.Unmarshal([]byte(body), &req) != nil || len(req.Messages) == 0 {
		return ""
	}
	return req.Messages[len(req.Messages)-1].Role
}

func toolResultsOf(t *testing.T, acli *sdk.Angelus, id string) []string {
	t.Helper()
	resp, err := acli.IRAll(t.Context(), id, 0)
	require.NoError(t, err)
	var out []string
	for _, e := range resp.Entries {
		var m message.Message
		if json.Unmarshal(e.Payload, &m) != nil {
			continue
		}
		for _, c := range m.Content {
			if c.Type == message.ContentToolResult {
				out = append(out, c.Text)
			}
		}
	}
	return out
}

// Defends #114: a replay never runs a tool. The recorded call gets the
// recorded answer, an outward verb a quiet success, anything else a
// neutral failure. The calls here must stay harmless if run for real: the
// test daemon's tools inherit the host's figaro environment.
func TestAReplayAnswersToolsFromTheRecordNeverTheHost(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "replay-ran-me")
	clock := `{"command":"date +%s%N"}`
	var branch string
	var branchMu sync.Mutex
	tap := &wireTap{reply: func(id, body string) string {
		switch {
		case lastRole(body) == "tool":
			return chatText("done")
		case !strings.Contains(body, "second question"):
			return chatText("ok")
		}
		branchMu.Lock()
		defer branchMu.Unlock()
		if id != branch {
			return chatToolCalls([2]string{"bash", clock})
		}
		return chatToolCalls(
			[2]string{"bash", clock},
			[2]string{"bash", `{"command":"figaro --help"}`},
			[2]string{"bash", fmt.Sprintf(`{"command":"touch %s"}`, marker)},
			[2]string{"read", fmt.Sprintf(`{"path":%q}`, marker)},
		)
	}}
	a, acli := gatewayDaemon(t, tap)
	created, err := acli.Create(t.Context(), dress(t, "gw"), nil)
	require.NoError(t, err)
	parent := created.FigaroID
	askAt(t, a, parent, "first question", "Thursday, November 12, 2026, 9AM EST")
	askAt(t, a, parent, "second question", "Thursday, November 12, 2026, 2PM EST")
	recorded := toolResultsOf(t, acli, parent)
	require.Len(t, recorded, 1, "the original turn ran its one tool")

	branchMu.Lock()
	resp, err := acli.Replay(t.Context(), parent, 2, nil, nil)
	if err == nil {
		branch = resp.Alternative
	}
	branchMu.Unlock()
	require.NoError(t, err)
	awaitWire(t, a, tap, branch, 2)

	require.Equal(t, []string{
		recorded[0],
		"(no output)",
		"(no output)",
		"Error: resource temporarily unavailable",
	}, toolResultsOf(t, acli, branch))
	_, statErr := os.Stat(marker)
	require.True(t, os.IsNotExist(statErr), "a replayed tool ran on the host")
}

func TestAReplayingFigaroTakesNoMessagesAndHoldsNoRole(t *testing.T) {
	a, acli, ctx, parent := replayFixture(t)
	resp, err := acli.Replay(ctx, parent, 2, nil, nil)
	require.NoError(t, err)
	branch := resp.Alternative
	require.Eventually(t, func() bool { return answeredTurns(t, acli, ctx, branch) == 2 }, 10*time.Second, 20*time.Millisecond)
	waitIdle(t, a, branch)

	c, err := sdk.DialAria(ariaAddr(a, branch), nil)
	require.NoError(t, err)
	defer c.Close()
	_, _, err = c.Qua(ctx, "are you there?", nil)
	require.ErrorContains(t, err, "takes no messages")
	role, err := acli.FormCreate(ctx, "", nil, rawPatch(map[string]string{"duty": `"answer"`}))
	require.NoError(t, err)
	_, err = c.Cast(ctx, rpc.CastRequest{FormID: role.FormID})
	require.ErrorContains(t, err, "holds no role")
}

// Defends #114: replay is not for life. It brings no lifetime of its own,
// and a plain fork of a replaying figaro is an ordinary figaro.
func TestReplayIsNotInheritedAndSetsNoLifetime(t *testing.T) {
	a, acli, ctx, parent := replayFixture(t)
	resp, err := acli.Replay(ctx, parent, 2, nil, nil)
	require.NoError(t, err)
	branch := resp.Alternative
	require.Eventually(t, func() bool { return answeredTurns(t, acli, ctx, branch) == 2 }, 10*time.Second, 20*time.Millisecond)
	waitIdle(t, a, branch)
	board := boardOf(t, acli, ctx, branch)
	_, replaying := form.ReadReplay(board)
	require.True(t, replaying)
	require.False(t, board.Has("system.ttl"), "a replay sets no ttl")

	plain, err := acli.Fork(ctx, branch, 0, 0, nil, nil)
	require.NoError(t, err)
	child := boardOf(t, acli, ctx, plain.Alternative)
	require.False(t, child.Has(form.ReplayKey), "a plain fork inherited the replay")
	askAt(t, a, plain.Alternative, "a fresh question", "Tuesday, November 17, 2026, 10AM EST")
	marks := replayMarks(t, acli, plain.Alternative)
	require.Nil(t, marks[len(marks)-1], "the plain fork's turn ran under replay rules")
}
