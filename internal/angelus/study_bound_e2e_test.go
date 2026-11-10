package angelus_test

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/angelus"
	"github.com/jack-work/figaro/internal/provider"
	"github.com/jack-work/figaro/internal/store"
)

// studyRenderer answers like the mock and keeps the study reminders a real
// dialect would have rendered for the aria's whole history.
type studyRenderer struct {
	mu   sync.Mutex
	seen map[string][]string
}

func (r *studyRenderer) Name() string                                         { return "mock" }
func (r *studyRenderer) Fingerprint() string                                  { return "mock/study" }
func (r *studyRenderer) SetModel(string)                                      {}
func (r *studyRenderer) Models(context.Context) ([]provider.ModelInfo, error) { return nil, nil }

func (r *studyRenderer) Send(_ context.Context, in provider.SendInput, bus provider.Bus) error {
	var texts []string
	if _, err := provider.CatchUp(provider.CatchUpConfig{
		Log:        in.FigLog,
		Translator: store.NewMemLog[[]json.RawMessage](),
		Form:       in.Form,
		Studies:    in.Studies,
		Encode: func(m message.Message, snap form.Snapshot) ([]json.RawMessage, error) {
			texts = append(texts, provider.StudyReminderTexts(m, snap)...)
			return []json.RawMessage{json.RawMessage(`{}`)}, nil
		},
	}); err != nil {
		return err
	}
	r.mu.Lock()
	r.seen[in.AriaID] = texts
	r.mu.Unlock()
	msg := message.Message{Role: message.RoleOutput, Content: []message.Content{message.TextContent("ok")}, StopReason: message.StopEnd}
	bus.PushDelta(message.TextContent("ok"))
	bus.PushMessageEnd(string(msg.StopReason))
	bus.PushFigaro(msg)
	return nil
}

func (r *studyRenderer) blocks(aria, name string) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for _, s := range r.seen[aria] {
		if strings.HasPrefix(s, `<system-reminder name="`+name+`">`) {
			out = append(out, s)
		}
	}
	return out
}

func studyBoundFixture(t *testing.T) (*angelus.Angelus, *studyRenderer, *studyClient, *studyClient, string, string, context.Context) {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(dir+"/outfits", 0o700))
	require.NoError(t, os.WriteFile(dir+"/outfits/mock.toml", []byte("[system]\nprovider = \"mock\"\nmodel = \"mock-model\"\n"), 0o600))
	require.NoError(t, os.WriteFile(dir+"/config.toml", []byte("default_outfit = \"mock\"\n"), 0o600))
	r := &studyRenderer{seen: map[string][]string{}}
	a, acli, ctx, _ := daemonWithProvider(t, dir, nil, func() provider.Provider { return r })
	parent, err := acli.Create(ctx, dress(t, "mock"), nil)
	require.NoError(t, err)
	child, err := acli.Create(ctx, dress(t, "mock"), nil)
	require.NoError(t, err)
	return a, r, dialNode(t, a, parent.FigaroID), dialNode(t, a, child.FigaroID), parent.FigaroID, child.FigaroID, ctx
}

func commentAndMantra(t *testing.T, ctx context.Context, a *angelus.Angelus, pc *studyClient, parent string) {
	t.Helper()
	_, err := pc.c.Set(ctx, *rawPatch(map[string]string{
		"comments.c1.body": `"why this design?"`,
		"mantra":           `"parent busy"`,
	}), 0)
	require.NoError(t, err)
	lib, err := a.Backend.(*store.XwalBackend).Libretto(parent)
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		_, ok := lib.State().Get("comments.c1.body")
		return ok
	}, 5*time.Second, 5*time.Millisecond, "the libretto never folded the parent's comment")
}

// A figaro's bound form is followed and rendered like an unbound form's:
// user keys arrive, system keys do not.
func TestAStudiedParentsChangeArrivesAsOneReminder(t *testing.T) {
	a, r, pc, cc, parent, child, ctx := studyBoundFixture(t)
	callCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	_, err := cc.c.Study(callCtx, parent)
	require.NoError(t, err)
	commentAndMantra(t, callCtx, a, pc, parent)
	cc.turn(t, callCtx, "anything for me?")

	got := r.blocks(child, "study:"+parent)
	require.Len(t, got, 1, "%q", got)
	require.Contains(t, got[0], "why this design?")
	require.Contains(t, got[0], "parent busy")
	require.NotContains(t, got[0], "system.")
}

func TestAFigaroStudyingItselfIsRefused(t *testing.T) {
	_, _, _, cc, parent, child, ctx := studyBoundFixture(t)
	callCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	_, err := cc.c.Study(callCtx, child)
	require.ErrorContains(t, err, "does not study its own bound form")
	_, err = cc.c.Study(callCtx, parent)
	require.NoError(t, err, "control: another figaro is studiable")
}

func TestCastOfAFigaroIsStillRefused(t *testing.T) {
	_, _, _, cc, parent, _, ctx := studyBoundFixture(t)
	callCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	_, err := cc.c.Cast(callCtx, rpc.CastRequest{FormID: parent})
	require.ErrorContains(t, err, "figaro, not an unbound form")
	_, err = cc.c.Study(callCtx, parent)
	require.NoError(t, err, "control: the same figaro is studiable")
}
