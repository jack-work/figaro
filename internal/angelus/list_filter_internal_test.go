package angelus

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/arialog"
	"github.com/jack-work/figaro/internal/authz"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/store"
	"github.com/jack-work/jkauthz"
	"github.com/jack-work/jkrpc"
)

type filterRig struct {
	hs                               *handlers
	hmap                             map[string]jkrpc.HandlerFunc
	root, child, grand, other, aside string
	owner, reader                    context.Context
}

func newFilterRig(t *testing.T) *filterRig {
	t.Helper()
	backend, err := store.NewXwalBackend(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = backend.Close() })
	outfit, err := backend.CreateOutfit("o", form.Build(form.Snapshot{}, map[string]json.RawMessage{
		"system.provider": json.RawMessage(`"p"`),
	}, nil))
	if err != nil {
		t.Fatal(err)
	}
	birth := func(parent, mantra string) string {
		t.Helper()
		id, _, err := backend.ForkWith(parent, 0, form.Build(form.Snapshot{}, map[string]json.RawMessage{
			"mantra": json.RawMessage(fmt.Sprintf("%q", mantra)),
		}, nil))
		if err != nil {
			t.Fatal(err)
		}
		if err := backend.UpdateMeta(id, func(m *store.AriaMeta) { m.Mantra = mantra }); err != nil {
			t.Fatal(err)
		}
		return id
	}
	r := &filterRig{}
	r.root = birth(outfit, "the harbour master")
	r.child = birth(r.root, "tide tables")
	r.grand = birth(r.child, "Lighthouse keeper")
	r.other = birth(outfit, "market stall")
	r.aside = birth(outfit, "tide pools")

	roleName, _ := json.Marshal("keeper")
	target, _ := json.Marshal(r.grand)
	if _, _, err := backend.CreateForm("", form.Build(form.Snapshot{}, map[string]json.RawMessage{
		"name": roleName, "target-aria": target,
	}, nil)); err != nil {
		t.Fatal(err)
	}

	for i, id := range []string{r.root, r.child} {
		log, err := backend.OpenFigIR(id)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := log.Append(arialog.Entry[message.Message]{Payload: message.Message{Role: message.RoleInput, Content: []message.Content{message.TextContent("hi")}, TurnID: uint64(3 + i*4)}}); err != nil {
			t.Fatal(err)
		}
	}

	a := &Angelus{Registry: NewRegistry(), Backend: backend, Hubs: newHubs()}
	a.Policies = authz.NewPolicies(jkauthz.Principal{Kind: jkauthz.KindUser, ID: "owner"}, StoreFacts(backend))
	policy := filepath.Join(t.TempDir(), "policy.toml")
	reads := ""
	for _, id := range []string{r.root, r.child, r.other, r.aside} {
		reads += fmt.Sprintf("%q, ", "read:aria:"+id)
	}
	body := "[roles.owner]\npermissions = [\"*:*\"]\n[[bind]]\nrole = \"owner\"\nprincipals = [\"user:owner\"]\n" +
		"[roles.reader]\npermissions = [" + reads + "]\n[[bind]]\nrole = \"reader\"\nprincipals = [\"user:reader\"]\n"
	if err := os.WriteFile(policy, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := a.Policies.Reload(policy); err != nil {
		t.Fatal(err)
	}
	hs := NewHandlers(ServerConfig{Angelus: a, Config: &config.Loaded{}, Ctx: inProcess(context.Background())})
	r.hs = &handlers{angelus: a}
	r.hmap = hs.Map
	r.owner = authz.WithPrincipal(inProcess(context.Background()), rpc.Principal{Kind: rpc.PrincipalUser, ID: "owner"})
	r.reader = authz.WithPrincipal(inProcess(context.Background()), rpc.Principal{Kind: rpc.PrincipalUser, ID: "reader"})
	return r
}

func (r *filterRig) list(t *testing.T, ctx context.Context, req rpc.ListRequest) rpc.ListResponse {
	t.Helper()
	raw, err := rpc.WithCaller(req, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	out, err := r.hmap[rpc.MethodList](ctx, raw)
	if err != nil {
		t.Fatalf("list %+v: %v", req, err)
	}
	return out.(rpc.ListResponse)
}

func ids(l rpc.ListResponse) []string {
	var out []string
	for _, f := range l.Figaros {
		out = append(out, f.ID)
	}
	slices.Sort(out)
	return out
}

func sorted(v ...string) []string {
	slices.Sort(v)
	return v
}

func TestListFiltersApplyBeforeTheyPage(t *testing.T) {
	r := newFilterRig(t)
	t.Run("under a root keeps its descendants only", func(t *testing.T) {
		got := ids(r.list(t, r.owner, rpc.ListRequest{Under: r.root}))
		if want := sorted(r.child, r.grand); !slices.Equal(got, want) {
			t.Fatalf("under root: %v, want %v", got, want)
		}
	})
	t.Run("holders keeps arias that hold a role", func(t *testing.T) {
		got := r.list(t, r.owner, rpc.ListRequest{Holders: true})
		if want := []string{r.grand}; !slices.Equal(ids(got), want) {
			t.Fatalf("holders: %v, want %v", ids(got), want)
		}
		if roles := got.Figaros[0].Roles; !slices.Equal(roles, []string{"@keeper"}) {
			t.Fatalf("roles: %v", roles)
		}
	})
	t.Run("match reads mantra without case", func(t *testing.T) {
		got := ids(r.list(t, r.owner, rpc.ListRequest{Match: "TIDE"}))
		if want := sorted(r.child, r.aside); !slices.Equal(got, want) {
			t.Fatalf("match tide: %v, want %v", got, want)
		}
	})
	t.Run("filters combine", func(t *testing.T) {
		got := ids(r.list(t, r.owner, rpc.ListRequest{Under: r.root, Match: "light"}))
		if want := []string{r.grand}; !slices.Equal(got, want) {
			t.Fatalf("under root matching light: %v, want %v", got, want)
		}
	})
	t.Run("a page is a page of the filtered list", func(t *testing.T) {
		var seen []string
		req := rpc.ListRequest{Match: "t", Limit: 1}
		for range 10 {
			page := r.list(t, r.owner, req)
			seen = append(seen, ids(page)...)
			if page.NextBefore == "" {
				break
			}
			req.Before = page.NextBefore
		}
		want := ids(r.list(t, r.owner, rpc.ListRequest{Match: "t"}))
		slices.Sort(seen)
		if !slices.Equal(seen, want) || len(want) < 3 {
			t.Fatalf("paged %v, whole %v", seen, want)
		}
	})
}

func TestListTurnsNamesTheNewestTurnOfTheRowsReturned(t *testing.T) {
	r := newFilterRig(t)
	turn := map[string]uint64{}
	for _, f := range r.list(t, r.owner, rpc.ListRequest{Turns: true}).Figaros {
		turn[f.ID] = f.LastTurn
	}
	if turn[r.root] != 3 || turn[r.child] != 7 || turn[r.other] != 0 {
		t.Fatalf("newest turns %v, want root 3, child 7, other 0", turn)
	}
	for _, f := range r.list(t, r.owner, rpc.ListRequest{}).Figaros {
		if f.LastTurn != 0 {
			t.Fatalf("%s carries turn %d though none was asked for", f.ID, f.LastTurn)
		}
	}
}

func TestListFiltersNeverShowWhatTheCallerCannotRead(t *testing.T) {
	r := newFilterRig(t)
	for name, req := range map[string]rpc.ListRequest{
		"plain":   {},
		"under":   {Under: r.root},
		"holders": {Holders: true},
		"match":   {Match: "light"},
		"turns":   {Turns: true},
	} {
		if slices.Contains(ids(r.list(t, r.reader, req)), r.grand) {
			t.Errorf("%s: the reader, who may not read %s, was shown it", name, r.grand)
		}
	}
	if got := ids(r.list(t, r.reader, rpc.ListRequest{Under: r.root})); !slices.Equal(got, []string{r.child}) {
		t.Fatalf("reader under root: %v, want only the child", got)
	}
	if got := r.list(t, r.reader, rpc.ListRequest{Holders: true}); len(got.Figaros) != 0 {
		t.Fatalf("reader sees role holders %v though it reads none", ids(got))
	}
	if got := r.list(t, r.owner, rpc.ListRequest{Match: "light"}); !slices.Equal(ids(got), []string{r.grand}) {
		t.Fatalf("control: the owner matching light: %v", ids(got))
	}
}
