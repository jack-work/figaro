package angelus

import (
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/authz"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/figaro/wire"
	"github.com/jack-work/figaro/internal/hierarchy/hierarchytest"
	"github.com/jack-work/figaro/internal/store"
)

type placedStore struct {
	t       *testing.T
	root    string
	backend *store.XwalBackend
	hs      *Handlers
}

func openPlacedStore(t *testing.T, root string) *placedStore {
	t.Helper()
	backend, err := store.NewXwalBackend(root, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := wire.Install(backend.Store(), root, wire.Capabilities{Trunks: true}); err != nil {
		t.Fatal(err)
	}
	a := &Angelus{Registry: NewRegistry(), Backend: backend}
	s := &placedStore{t: t, root: root, backend: backend}
	s.hs = NewHandlers(ServerConfig{Angelus: a, Config: &config.Loaded{}, Ctx: inProcess(context.Background())})
	t.Cleanup(func() { backend.Close() })
	return s
}

func (s *placedStore) reopen() *placedStore {
	s.t.Helper()
	if err := s.backend.Close(); err != nil {
		s.t.Fatal(err)
	}
	return openPlacedStore(s.t, s.root)
}

func (s *placedStore) grant(id string, scopes ...string) {
	s.t.Helper()
	raw, _ := json.Marshal(scopes)
	if _, err := s.backend.ApplyForm(id, form.Build(form.Snapshot{}, map[string]json.RawMessage{form.ScopesKey: raw}, nil)); err != nil {
		s.t.Fatal(err)
	}
}

func (s *placedStore) fork(of string, scopes ...string) string {
	s.t.Helper()
	_, alt, err := s.backend.Fork(of)
	if err != nil {
		s.t.Fatal(err)
	}
	s.grant(alt, scopes...)
	return alt
}

func (s *placedStore) turns(id string, n int) []uint64 {
	s.t.Helper()
	log, err := s.backend.OpenFigIR(id)
	if err != nil {
		s.t.Fatal(err)
	}
	var opens []uint64
	for i := 0; i < n; i++ {
		for _, role := range []message.Role{message.RoleInput, message.RoleOutput} {
			e, err := log.Append(store.Entry[message.Message]{Payload: message.Message{Role: role, Content: []message.Content{message.TextContent("row")}}})
			if err != nil {
				s.t.Fatal(err)
			}
			if role == message.RoleInput {
				opens = append(opens, e.LT)
			}
		}
	}
	return opens
}

func (s *placedStore) effective(id string) (string, bool) {
	s.t.Helper()
	p := authz.NewPolicies(jkauthzOwner(), StoreFacts(s.backend))
	if err := p.Reload(""); err != nil {
		s.t.Fatal(err)
	}
	set, held := p.Effective(id)
	return set.String(), held
}

func (s *placedStore) call(method string, req any, into any) {
	s.t.Helper()
	raw, _ := json.Marshal(req)
	out, err := s.hs.Map[method](inProcess(context.Background()), raw)
	if err != nil {
		s.t.Fatalf("%s: %v", method, err)
	}
	sent, _ := json.Marshal(out)
	if err := json.Unmarshal(sent, into); err != nil {
		s.t.Fatal(err)
	}
}

// nodeMarker is a figaro's .node file as it lies on disk. The tests below
// rewrite it by hand, with the store closed, to build what no current code
// path writes: the fork point an older release rewrote, and names of nodes
// that are gone.
type nodeMarker struct {
	path   string
	key    string
	fields map[string]string
}

func (s *placedStore) marker(id string) nodeMarker {
	s.t.Helper()
	var found []nodeMarker
	err := filepath.WalkDir(s.root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || d.Name() != ".node" {
			return err
		}
		body, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		m := nodeMarker{path: p, key: filepath.Base(filepath.Dir(p)), fields: map[string]string{}}
		for _, line := range strings.Split(strings.TrimSuffix(string(body), "\n"), "\n") {
			k, v, _ := strings.Cut(line, "=")
			m.fields[k] = v
		}
		if m.fields["trunk"] == id {
			found = append(found, m)
		}
		return nil
	})
	if err != nil || len(found) != 1 {
		s.t.Fatalf("figaro %s has %d node markers under %s (%v), want one", id, len(found), s.root, err)
	}
	return found[0]
}

func (m nodeMarker) rewrite(t *testing.T, from string, placed ...string) {
	t.Helper()
	body := ""
	for _, p := range placed {
		body += "placed=" + p + "\n"
	}
	body += "from=" + from + "\nkind=" + m.fields["kind"] + "\ntrunk=" + m.fields["trunk"] + "\n"
	if err := os.WriteFile(m.path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// 0.63.0 to 0.65.4 seated a fork cut inside an ancestor's turns by naming
// its lead as its fork point. doctor restore-forks names the holder of the
// cut again and records the lead as the placement: the dry run lists the
// figaro and writes nothing, the real run changes no authority fact of any
// node, and a second run finds nothing.
func TestRestoreForksNamesTheHolderOfTheCutAndChangesNoAuthority(t *testing.T) {
	s := openPlacedStore(t, t.TempDir())
	outfit, err := s.backend.CreateOutfit("o", form.Patch{})
	if err != nil {
		t.Fatal(err)
	}
	top, err := s.backend.CreateConversation(outfit)
	if err != nil {
		t.Fatal(err)
	}
	s.grant(top, "*:*", "egress:*")
	opens := s.turns(top, 3)
	_, lead, err := s.backend.ForkAt(top, opens[2])
	if err != nil {
		t.Fatal(err)
	}
	s.grant(lead, "read:*", "egress:a:1")
	s.turns(lead, 1)
	_, builder, err := s.backend.ForkAt(top, opens[1])
	if err != nil {
		t.Fatal(err)
	}
	s.grant(builder, "*:*", "egress:*")
	natural := s.fork(lead, "*:*", "egress:*")
	if err := hierarchytest.Of(s.backend).Move(builder, lead); err != nil {
		t.Fatalf("seat the builder under its lead: %v", err)
	}
	holder, _ := s.backend.Node(builder)
	if holder.ForkPoint == lead || holder.Parent != lead {
		t.Fatalf("setup: the builder was cut from %s and answers to %s, want another fork point and the lead %s", holder.ForkPoint, holder.Parent, lead)
	}
	seated, leadMarker := s.marker(builder), s.marker(lead)
	if err := s.backend.Close(); err != nil {
		t.Fatal(err)
	}
	seated.rewrite(t, leadMarker.key)
	s = openPlacedStore(t, s.root)

	if n, _ := s.backend.Node(builder); n.ForkPoint != lead || n.Parent != lead {
		t.Fatalf("setup: the older release's store reads the builder as cut from %s and answering to %s, want the lead %s for both", n.ForkPoint, n.Parent, lead)
	}
	wide, _ := s.effective(top)
	if got, held := s.effective(builder); !held || got == wide {
		t.Fatalf("setup: the builder holds %s (held %v): its lead narrows nothing, so the test would prove nothing", got, held)
	}
	before := authorityLines(t, s.backend)

	var dry rpc.RestoreForksResponse
	s.call(rpc.MethodForksRestore, rpc.RestoreForksRequest{DryRun: true}, &dry)
	if !dry.DryRun || len(dry.Kept) != 0 || len(dry.Restored) != 1 {
		t.Fatalf("dry run = %+v, want the builder alone", dry)
	}
	if r := dry.Restored[0]; r.ID != builder || r.Handle == "" || r.Was != lead || r.WasHandle == "" || r.ForkPoint != holder.ForkPoint || r.ForkPointHandle == "" {
		t.Fatalf("dry run names %+v, want %s moving its fork point from %s to %s, each with its handle", r, builder, lead, holder.ForkPoint)
	}
	if n, _ := s.backend.Node(builder); n.ForkPoint != lead {
		t.Fatalf("a dry run rewrote the fork point to %s", n.ForkPoint)
	}

	var done rpc.RestoreForksResponse
	s.call(rpc.MethodForksRestore, rpc.RestoreForksRequest{}, &done)
	if done.DryRun || len(done.Kept) != 0 || len(done.Restored) != 1 || done.Restored[0].ID != builder {
		t.Fatalf("restore = %+v, want the builder alone", done)
	}
	after := authorityLines(t, s.backend)
	if len(before) != len(after) {
		t.Fatalf("the restore changed the number of authority facts: %d before, %d after", len(before), len(after))
	}
	for i := range before {
		if before[i] != after[i] {
			t.Fatalf("the restore changed an authority fact:\nbefore %safter  %s", before[i], after[i])
		}
	}
	if n, _ := s.backend.Node(builder); n.ForkPoint != holder.ForkPoint || n.Parent != lead || n.Unplaced {
		t.Fatalf("after the restore the builder was cut from %s and answers to %s, want %s and the lead %s", n.ForkPoint, n.Parent, holder.ForkPoint, lead)
	}
	if n, _ := s.backend.Node(natural); n.ForkPoint != lead || n.Parent != lead {
		t.Fatalf("a fork cut in its parent's own rows was moved: cut from %s, answers to %s", n.ForkPoint, n.Parent)
	}

	var again rpc.RestoreForksResponse
	s.call(rpc.MethodForksRestore, rpc.RestoreForksRequest{}, &again)
	if len(again.Restored) != 0 || len(again.Kept) != 0 {
		t.Fatalf("a second restore found %+v", again)
	}
	s = s.reopen()
	if n, _ := s.backend.Node(builder); n.ForkPoint != holder.ForkPoint || n.Parent != lead {
		t.Fatalf("after a restart the builder was cut from %s and answers to %s, want %s and the lead %s", n.ForkPoint, n.Parent, holder.ForkPoint, lead)
	}
}

// doctor placements is the owner's list before a downgrade and after a
// crash: who answers to a figaro other than its fork point, whose placement
// names a node that is gone (it holds nothing), and whose fork point names
// no node (it reads as top-level, as it always did).
func TestPlacementsListsPlacedLostAndDanglingForkPoints(t *testing.T) {
	s := openPlacedStore(t, t.TempDir())
	outfit, err := s.backend.CreateOutfit("o", form.Patch{})
	if err != nil {
		t.Fatal(err)
	}
	top, err := s.backend.CreateConversation(outfit)
	if err != nil {
		t.Fatal(err)
	}
	s.grant(top, "*:*", "egress:*")
	narrowed := s.fork(top, "read:*", "egress:a:1")
	placed := s.fork(top, "*:*", "egress:*")
	lost := s.fork(top, "*:*", "egress:*")
	dangling := s.fork(narrowed, "*:*", "egress:*")
	plain := s.fork(narrowed, "*:*", "egress:*")
	if err := hierarchytest.Of(s.backend).Move(placed, narrowed); err != nil {
		t.Fatalf("place a figaro under the narrowed one: %v", err)
	}
	wide, _ := s.effective(top)
	lostMarker, danglingMarker := s.marker(lost), s.marker(dangling)
	if err := s.backend.Close(); err != nil {
		t.Fatal(err)
	}
	const gonePlacement, goneFork = "n999001", "n999002"
	lostMarker.rewrite(t, lostMarker.fields["from"], gonePlacement)
	danglingMarker.rewrite(t, goneFork)
	s = openPlacedStore(t, s.root)

	var resp rpc.PlacementsResponse
	s.call(rpc.MethodPlacements, struct{}{}, &resp)
	if len(resp.Placed) != 1 || len(resp.Lost) != 1 || len(resp.Dangling) != 1 {
		t.Fatalf("placements = %+v, want one placed, one lost, one dangling", resp)
	}
	if p := resp.Placed[0]; p.ID != placed || p.Handle == "" || p.Under != narrowed || p.UnderHandle == "" || p.ForkPoint != top || p.Names != "" {
		t.Fatalf("placed = %+v, want %s under %s, cut from %s", p, placed, narrowed, top)
	}
	if p := resp.Lost[0]; p.ID != lost || p.Handle == "" || p.Names != gonePlacement || p.Under != "" || p.ForkPoint != top {
		t.Fatalf("lost = %+v, want %s naming %s, cut from %s", p, lost, gonePlacement, top)
	}
	if p := resp.Dangling[0]; p.ID != dangling || p.Handle == "" || p.Names != goneFork {
		t.Fatalf("dangling = %+v, want %s naming %s", p, dangling, goneFork)
	}

	if got, held := s.effective(lost); held {
		t.Fatalf("a figaro whose placement names a node that is gone holds %s, want nothing", got)
	}
	if got, held := s.effective(dangling); !held || got != wide {
		t.Fatalf("a figaro whose fork point names no node holds %s (held %v), want its own scopes %s as a top-level figaro", got, held, wide)
	}
	if got, held := s.effective(plain); !held || got == wide {
		t.Fatalf("a figaro forked from the narrowed one holds %s (held %v): the fixture narrows nothing", got, held)
	}
}
