package angelus

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/authz"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/jkauthz"
)

func resolveHandlers(t testing.TB) (*handlers, *Handlers) {
	t.Helper()
	a := &Angelus{Registry: NewRegistry(), Backend: store.NewTestBackend(t)}
	hs := NewHandlers(ServerConfig{Angelus: a, Config: &config.Loaded{}, Ctx: context.Background()})
	return hs.h, hs
}

func mintConversation(t testing.TB, h *handlers) string {
	t.Helper()
	stump, err := h.angelus.Backend.CreateOutfit("test", message.Patch{})
	if err != nil {
		t.Fatal(err)
	}
	id, err := h.angelus.Backend.CreateConversation(stump)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestNewIDsAre128Bits(t *testing.T) {
	h, _ := resolveHandlers(t)
	id := mintConversation(t, h)
	if len(id) != 32 || strings.Trim(id, "0123456789abcdef") != "" {
		t.Fatalf("minted id %q, want 32 hex", id)
	}
}

// A prefix the store can name once is that aria, at every door method.
func TestAPrefixResolvesToItsAria(t *testing.T) {
	h, hs := resolveHandlers(t)
	id := mintConversation(t, h)

	for _, ref := range []string{id, id[:8], id[:4], h.nodeID() + "/" + id[:8], h.nodeID()[:8] + "/" + id} {
		got, err := h.canonicalID(context.Background(), ref)
		if err != nil || got != id {
			t.Fatalf("canonicalID(%q) = %q, %v; want %q", ref, got, err, id)
		}
	}

	out, err := hs.Map[rpc.MethodAttach](context.Background(), json.RawMessage(`{"figaro_id":"`+id[:8]+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := out.(rpc.AttachResponse).FigaroID; got != id {
		t.Fatalf("attach by prefix answered %q, want %q", got, id)
	}
}

func TestAnotherNodeIsNotThisOne(t *testing.T) {
	h, hs := resolveHandlers(t)
	id := mintConversation(t, h)
	_, err := hs.Map[rpc.MethodAttach](context.Background(), json.RawMessage(`{"figaro_id":"0e6f7dbf-0000-4000-8000-000000000000/`+id+`"}`))
	if err == nil || !strings.Contains(err.Error(), "this is node "+h.nodeID()) {
		t.Fatalf("err = %v, want a refusal naming both nodes", err)
	}
}

type fixedIDs struct {
	store.Backend
	ids []string
}

func (f fixedIDs) Node(id string) (store.NodeView, bool) {
	for _, x := range f.ids {
		if x == id {
			return store.NodeView{ID: id}, true
		}
	}
	return store.NodeView{}, false
}

func (f fixedIDs) MatchID(prefix string, limit int) []string {
	var out []string
	for _, x := range f.ids {
		if strings.HasPrefix(x, prefix) && (limit < 0 || len(out) < limit) {
			out = append(out, x)
		}
	}
	return out
}

// An old 8-hex id must keep resolving to itself even when a new 32-hex id
// starts with it; only a longer prefix reaches the new one.
func TestPrefixResolutionAcrossBothWidths(t *testing.T) {
	h, _ := resolveHandlers(t)
	old := "abcd1234"
	long := "abcd1234" + strings.Repeat("e", 24)
	other := "abcd9999" + strings.Repeat("f", 24)
	h.angelus.Backend = fixedIDs{Backend: h.angelus.Backend, ids: []string{old, long, other}}

	cases := []struct{ ref, want, err string }{
		{ref: old, want: old},
		{ref: "abcd1234e", want: long},
		{ref: "abcd9", want: other},
		{ref: "abcd", err: "ambiguous"},
		{ref: "abc", want: "abc"},
	}
	for _, c := range cases {
		got, err := h.canonicalID(context.Background(), c.ref)
		if c.err != "" {
			if err == nil || !strings.Contains(err.Error(), c.err) || !strings.Contains(err.Error(), old) {
				t.Errorf("canonicalID(%q) err = %v, want %q naming the candidates", c.ref, err, c.err)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("canonicalID(%q) = %q, %v; want %q", c.ref, got, err, c.want)
		}
	}
}

func TestSecondaryIDsResolveToo(t *testing.T) {
	h, _ := resolveHandlers(t)
	long := "abcd1234" + strings.Repeat("e", 24)
	form := "@" + "9876" + strings.Repeat("a", 28)
	h.angelus.Backend = fixedIDs{Backend: h.angelus.Backend, ids: []string{long, form}}
	out, err := h.canonicalParams(context.Background(), json.RawMessage(`{"figaro_id":"abcd1234","form_id":"@9876","against":"abcd12"}`))
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]string
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	if got["figaro_id"] != long || got["form_id"] != form || got["against"] != long {
		t.Fatalf("canonical params = %v", got)
	}
}

// Resolution runs before the policy, so it must not become a way to learn
// ids: a prefix names only arias the caller may read, and one that matches
// nothing readable stays the caller's own string, in the answer and in any
// refusal.
func TestAPrefixResolvesOnlyAmongWhatTheCallerMayRead(t *testing.T) {
	h, hs := resolveHandlers(t)
	mine := "abcd1234" + strings.Repeat("e", 24)
	theirs := "abcd9999" + strings.Repeat("f", 24)
	h.angelus.Backend = fixedIDs{Backend: h.angelus.Backend, ids: []string{mine, theirs}}
	h.angelus.Policies = authz.NewPolicies(jkauthz.Principal{Kind: jkauthz.KindUser, ID: "owner"}, nil)
	policy := filepath.Join(t.TempDir(), "policy.toml")
	if err := os.WriteFile(policy, []byte(`
[roles.owner]
grants = [{ verbs = ["*"], on = ["*"] }]
[roles.guest]
grants = [{ verbs = ["read"], on = ["aria:`+mine+`"] }]
[[bind]]
role = "owner"
principals = ["user:owner"]
[[bind]]
role = "guest"
principals = ["user:guest"]
`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := h.angelus.Policies.Reload(policy); err != nil {
		t.Fatal(err)
	}
	owner := authz.WithPrincipal(context.Background(), rpc.Principal{Kind: rpc.PrincipalUser, ID: "owner"})
	guest := authz.WithPrincipal(context.Background(), rpc.Principal{Kind: rpc.PrincipalUser, ID: "guest"})

	if _, err := h.canonicalID(owner, "abcd"); err == nil || !strings.Contains(err.Error(), theirs) {
		t.Fatalf("the owner reads both, so abcd is ambiguous: %v", err)
	}
	if got, err := h.canonicalID(guest, "abcd"); err != nil || got != mine {
		t.Fatalf("the guest reads one, so abcd is %s: got %q, %v", mine, got, err)
	}
	if got, err := h.canonicalID(guest, "abcd9"); err != nil || got != "abcd9" {
		t.Fatalf("a prefix of an aria the guest cannot read resolved to %q, %v", got, err)
	}
	_, err := hs.Map[rpc.MethodAttach](guest, json.RawMessage(`{"figaro_id":"abcd9"}`))
	if err == nil || strings.Contains(err.Error(), theirs) {
		t.Fatalf("the refusal named the hidden aria: %v", err)
	}
}
