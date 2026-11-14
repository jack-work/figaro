package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
	fwtree "github.com/jack-work/figaro/figwal/tree"
	"github.com/jack-work/figaro/figwal/xwal"
)

// Lineage comes from the topology snapshot. The oracle is the walk it
// replaced: InfoLight per link, reading .fork each time. Every read an aria
// makes (its own history and what goes to the provider) must cut the same
// way under both, after every kind of topology move, on a handle that was
// open through the move and on one opened after it.

func oracleLineage(s *XwalStore, id string) ([]fwtree.Ref, error) {
	var refs []fwtree.Ref
	seen := map[string]bool{}
	for cur := id; cur != "" && !seen[cur]; {
		seen[cur] = true
		ti, ok := s.trunks.InfoLight(cur)
		if !ok {
			break
		}
		if ti.BaseErr != nil {
			return nil, ti.BaseErr
		}
		refs = append(refs, fwtree.Ref{Node: ti.ID, Base: ti.BranchedLT})
		cur = ti.Parent
	}
	if len(refs) == 0 {
		return nil, nil
	}
	slices.Reverse(refs)
	refs[0].Base = 0
	return refs, nil
}

func presentBackend(t *testing.T) (*XwalBackend, string) {
	t.Helper()
	root := t.TempDir()
	b, err := NewXwalBackend(root, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { b.Close() })
	tree, err := OpenTopologyTree(b.store, root)
	if err != nil {
		t.Fatal(err)
	}
	b.store.SetTree(tree)
	b.SetDetachCarry(func([]string) error { return nil })
	outfit, err := b.CreateOutfit("d", form.Build(form.Snapshot{}, map[string]json.RawMessage{"system.model": json.RawMessage(`"m"`)}, nil))
	if err != nil {
		t.Fatal(err)
	}
	return b, outfit
}

const lineageProvider = "anthropic"

func writeTurns(t *testing.T, b *XwalBackend, id string, n int) {
	t.Helper()
	if err := appendTurns(b, id, n); err != nil {
		t.Fatal(err)
	}
}

func appendTurns(b *XwalBackend, id string, n int) error {
	ir, err := b.OpenFigIR(id)
	if err != nil {
		return err
	}
	tr, err := b.OpenTranslator(id, lineageProvider)
	if err != nil {
		return err
	}
	for i := 0; i < n; i++ {
		e, err := ir.Append(Entry[message.Message]{Payload: message.Message{Role: message.RoleInput, Content: []message.Content{message.TextContent(fmt.Sprintf("%s-%d", id, i))}}})
		if err != nil {
			return err
		}
		if _, err := tr.Append(Entry[[]json.RawMessage]{FigaroLT: e.LT, Payload: []json.RawMessage{json.RawMessage(fmt.Sprintf("%q", id))}}); err != nil {
			return err
		}
	}
	return nil
}

type readSet[T any] struct {
	Read []Entry[T]
	From []Entry[T]
	Scan []Entry[T]
	Tail []Entry[T]
}

func readCuts[T any](l Log[T]) readSet[T] {
	var rs readSet[T]
	rs.Read = rowsOnly(l.Read())
	rs.From = rowsOnly(l.ReadFrom(2, 0))
	if sc, ok := l.(interface {
		ScanRange(from, to uint64, yield func(Entry[T]) bool)
	}); ok {
		sc.ScanRange(0, 0, func(e Entry[T]) bool { rs.Scan = append(rs.Scan, e); return true })
	}
	rs.Scan = rowsOnly(rs.Scan)
	rs.Tail = rowsOnly(TailSnapshot(l, 3))
	return rs
}

// EncodedBytes is how the row reached this reader (zero when the writer's
// own append was seeded), not what the row says.
func rowsOnly[T any](rows []Entry[T]) []Entry[T] {
	out := make([]Entry[T], len(rows))
	for i, e := range rows {
		e.EncodedBytes = 0
		out[i] = e
	}
	return out
}

func detach(t *testing.T, tr interface {
	HeadNode(string) (string, bool)
	Detach(string) error
}, id string) error {
	node, ok := tr.HeadNode(id)
	if !ok {
		return fmt.Errorf("no node for %s", id)
	}
	return tr.Detach(node)
}

func irLogWith(b *XwalBackend, id string, lineage func() ([]fwtree.Ref, error)) *treeLog[message.Message] {
	open := func(node string) Log[message.Message] {
		return newXwalLog[message.Message](b.store, node, chanIR, true)
	}
	cache := NewIRCache[message.Message](fwtree.NewBudget(0), open, irEntrySize, irKey[message.Message])
	return newTreeLog[message.Message](open(id), id, cache, irEntrySize, irKey[message.Message], lineage).
		withNodeOpener(open).seedingTail()
}

func transLogWith(b *XwalBackend, id string, lineage func() ([]fwtree.Ref, error)) *treeLog[[]json.RawMessage] {
	ch := transChannel(lineageProvider)
	open := func(node string) Log[[]json.RawMessage] {
		return newXwalLog[[]json.RawMessage](b.store, node, ch, false)
	}
	cache := NewIRCache[[]json.RawMessage](fwtree.NewBudget(0), open, transEntrySize, transKey[[]json.RawMessage])
	return newTreeLog[[]json.RawMessage](open(id), id, cache, transEntrySize, transKey[[]json.RawMessage], lineage).
		seedingTail().withNodeOpener(open)
}

type ariaReads struct {
	IR    readSet[message.Message]
	Trans readSet[[]json.RawMessage]
}

func liveConversations(b *XwalBackend) []string {
	var ids []string
	for _, n := range b.Conversations() {
		ids = append(ids, n.ID)
	}
	slices.Sort(ids)
	return ids
}

type lineageRig struct {
	t      *testing.T
	b      *XwalBackend
	held   map[string]ariaReadsOpen
	checks int
}

type ariaReadsOpen struct {
	ir    Log[message.Message]
	trans Log[[]json.RawMessage]
}

func (r *lineageRig) hold(id string) {
	ir, err := r.b.OpenFigIR(id)
	if err != nil {
		r.t.Fatal(err)
	}
	tr, err := r.b.OpenTranslator(id, lineageProvider)
	if err != nil {
		r.t.Fatal(err)
	}
	r.held[id] = ariaReadsOpen{ir, tr}
}

func (r *lineageRig) check(step string) {
	r.t.Helper()
	s := r.b.store
	for _, id := range liveConversations(r.b) {
		want, err := oracleLineage(s, id)
		if err != nil {
			r.t.Fatalf("%s: oracle lineage of %s: %v", step, id, err)
		}
		got, err := s.Lineage(id)
		if err != nil {
			r.t.Fatalf("%s: lineage of %s: %v", step, id, err)
		}
		if !reflect.DeepEqual(got, want) {
			r.t.Fatalf("%s: lineage of %s is %v, the walk says %v", step, id, got, want)
		}
		var full []fwtree.Ref
		s.WithFullTopologyForTest(func() { full, _ = s.Lineage(id) })
		if !reflect.DeepEqual(got, full) {
			r.t.Fatalf("%s: lineage of %s is %v, a forced rebuild says %v", step, id, got, full)
		}
		oracle := func() ([]fwtree.Ref, error) { return oracleLineage(s, id) }
		snapshot := func() ([]fwtree.Ref, error) { return s.Lineage(id) }
		wantReads := ariaReads{readCuts[message.Message](irLogWith(r.b, id, oracle)), readCuts[[]json.RawMessage](transLogWith(r.b, id, oracle))}
		fresh := ariaReads{readCuts[message.Message](irLogWith(r.b, id, snapshot)), readCuts[[]json.RawMessage](transLogWith(r.b, id, snapshot))}
		if !reflect.DeepEqual(fresh, wantReads) {
			r.t.Fatalf("%s: a fresh handle on %s reads %+v, the walk reads %+v", step, id, fresh, wantReads)
		}
		if h, ok := r.held[id]; ok {
			open := ariaReads{readCuts(h.ir), readCuts(h.trans)}
			if !reflect.DeepEqual(open, wantReads) {
				r.t.Fatalf("%s: the open handle on %s reads %+v, the walk reads %+v", step, id, open, wantReads)
			}
		}
		if len(wantReads.IR.Read) == 0 {
			r.t.Fatalf("%s: %s reads nothing; the row proves nothing", step, id)
		}
		r.checks++
	}
}

func TestEveryReadCutsTheSameWayAfterEveryTopologyMove(t *testing.T) {
	b, outfit := presentBackend(t)
	r := &lineageRig{t: t, b: b, held: map[string]ariaReadsOpen{}}
	must := func(id string, err error) string {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	alt := func(_, a string, err error) string { t.Helper(); return must(a, err) }

	p := must(b.CreateConversation(outfit))
	writeTurns(t, b, p, 6)
	r.hold(p)
	r.check("create")

	c1 := alt(b.Fork(p))
	writeTurns(t, b, c1, 3)
	r.hold(c1)
	r.check("tail fork")

	c2 := alt(b.ForkAt(p, 3))
	writeTurns(t, b, c2, 2)
	r.hold(c2)
	r.check("interior fork")

	g := alt(b.ForkAt(c1, 8))
	writeTurns(t, b, g, 2)
	r.hold(g)
	r.check("interior fork of a fork")

	if _, err := b.Promote(g, 1); err != nil {
		t.Fatal(err)
	}
	r.check("promote")

	if _, err := b.Demote(c2, ""); err != nil {
		t.Fatal(err)
	}
	r.check("demote")

	heir, _, err := b.ForkWith(c1, 0, patchSet(map[string]string{"brief": "heir"}))
	if err != nil {
		t.Fatal(err)
	}
	writeTurns(t, b, heir, 1)
	if err := b.Arrange(map[string]string{heir: p, c1: heir}, nil, nil); err != nil {
		t.Fatal(err)
	}
	r.check("succession (fork, then arrange the heir above the holder)")

	if err := b.Move(c2, c1); err != nil && !errors.Is(err, xwal.ErrMoveCopies) {
		t.Fatal(err)
	} else if err == nil {
		r.check("move")
	} else {
		t.Logf("move refused, as it must when the inheritance differs: %v", err)
	}

	d := alt(b.Fork(c2))
	writeTurns(t, b, d, 2)
	r.hold(d)
	if err := detach(t, b.store.trunks, d); err != nil {
		t.Fatal(err)
	}
	r.check("detach")

	kid := alt(b.Fork(heir))
	writeTurns(t, b, kid, 1)
	if err := b.Arrange(map[string]string{kid: p, c1: p}, nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := b.Remove(heir, false); err != nil {
		t.Fatal(err)
	}
	delete(r.held, heir)
	r.check("kill --keep-children")

	if err := b.Remove(c1, true); err != nil {
		t.Fatal(err)
	}
	for id := range r.held {
		if _, ok := b.Node(id); !ok {
			delete(r.held, id)
		}
	}
	r.check("kill -r")

	t.Logf("%d aria checks", r.checks)
}

// The key covers three clocks. The root epoch moves when a Trunks opens on
// the root (it may have repaired or rebuilt) and on every Remove; nothing a
// production daemon does makes a second Trunks on a root, so these tests
// make one by hand. The version: this Trunks' own spawns take no root
// mutation, so the epoch does not move.
func TestTheSnapshotFollowsAPeerOpeningOnTheRoot(t *testing.T) {
	b, outfit := presentBackend(t)
	p, _ := b.CreateConversation(outfit)
	writeTurns(t, b, p, 4)
	_, c, err := b.Fork(p)
	if err != nil {
		t.Fatal(err)
	}
	writeTurns(t, b, c, 2)
	if refs, err := b.store.Lineage(c); err != nil || len(refs) != 2 {
		t.Fatalf("lineage of the fork = %v, %v", refs, err)
	}
	peer, err := b.store.trunks.PeerTrunksForTest()
	if err != nil {
		t.Fatal(err)
	}
	defer peer.Close()
	node, ok := b.store.trunks.HeadNode(c)
	if !ok {
		t.Fatal("no node for the fork")
	}
	if err := peer.Detach(node); err != nil {
		t.Fatal(err)
	}
	// Asked before the walk: InfoLight rebuilds this Trunks from disk, which
	// moves its version and would hide a key that ignores the epoch.
	got, err := b.store.Lineage(c)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := oracleLineage(b.store, c)
	if len(want) != 1 || !reflect.DeepEqual(got, want) {
		t.Fatalf("after a peer opened and detached %s: lineage %v, the walk says %v", c, got, want)
	}
}

func TestTheSnapshotFollowsAPeerRemove(t *testing.T) {
	b, outfit := presentBackend(t)
	p, _ := b.CreateConversation(outfit)
	writeTurns(t, b, p, 4)
	_, c, err := b.Fork(p)
	if err != nil {
		t.Fatal(err)
	}
	peer, err := b.store.trunks.PeerTrunksForTest()
	if err != nil {
		t.Fatal(err)
	}
	defer peer.Close()
	if refs, err := b.store.Lineage(c); err != nil || len(refs) != 2 {
		t.Fatalf("lineage of the fork after the peer opened = %v, %v", refs, err)
	}
	epoch := b.store.trunks.RootEpoch()
	if err := peer.Remove(c, false); err != nil {
		t.Fatal(err)
	}
	if b.store.trunks.RootEpoch() == epoch {
		t.Fatal("a Remove did not move the root epoch; this test no longer proves the key needs it")
	}
	if refs, err := b.store.Lineage(c); !errors.Is(err, xwal.ErrUnknownTrunk) {
		t.Fatalf("lineage of a fork a peer removed = %v, %v; want ErrUnknownTrunk", refs, err)
	}
}

func TestTheSnapshotFollowsThisTrunksSpawn(t *testing.T) {
	b, outfit := presentBackend(t)
	p, _ := b.CreateConversation(outfit)
	writeTurns(t, b, p, 4)
	if _, err := b.store.Lineage(p); err != nil {
		t.Fatal(err)
	}
	epoch := b.store.trunks.RootEpoch()
	_, c, err := b.Fork(p)
	if err != nil {
		t.Fatal(err)
	}
	if b.store.trunks.RootEpoch() != epoch {
		t.Fatal("a spawn moved the root epoch; this test no longer proves the version is needed")
	}
	refs, err := b.store.Lineage(c)
	if err != nil || len(refs) != 2 || refs[0].Node != p {
		t.Fatalf("lineage of a fresh fork = %v, %v", refs, err)
	}
}

func TestAnIDTheSnapshotDoesNotHoldRefuses(t *testing.T) {
	b, outfit := presentBackend(t)
	p, _ := b.CreateConversation(outfit)
	writeTurns(t, b, p, 4)
	peer, err := b.store.trunks.PeerTrunksForTest()
	if err != nil {
		t.Fatal(err)
	}
	defer peer.Close()
	if _, err := b.store.Lineage(p); err != nil {
		t.Fatal(err)
	}
	peerChild, err := peer.ForkTail(p)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{peerChild, "0123456789abcdef0123456789abcdef"} {
		if refs, err := b.store.Lineage(id); !errors.Is(err, xwal.ErrUnknownTrunk) || refs != nil {
			t.Fatalf("lineage of %s = %v, %v; want ErrUnknownTrunk", id, refs, err)
		}
		if links, err := b.store.LineageLinks(id); !errors.Is(err, xwal.ErrUnknownTrunk) || links != nil {
			t.Fatalf("links of %s = %v, %v; want ErrUnknownTrunk", id, links, err)
		}
		if _, err := b.OpenFigIR(id); err == nil {
			t.Fatalf("the IR of %s opened", id)
		}
		if _, err := b.OpenTranslator(id, lineageProvider); err == nil {
			t.Fatalf("the translation of %s opened", id)
		}
	}
}

// RecordBaseTurn writes .forkturn without moving any clock the snapshot is
// keyed on, so a turn kept in the snapshot would never be seen and every
// lineage call would recompute and rewrite it.
func TestTwoLineageWalksRecordAForkTurnOnce(t *testing.T) {
	b, outfit := presentBackend(t)
	p, _ := b.CreateConversation(outfit)
	writeTurns(t, b, p, 4)
	_, c, err := b.ForkAt(p, 3)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(filepath.Dir(irForkPath(t, b.root, c)), ".forkturn")); err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	writes := 0
	for i := 0; i < 2; i++ {
		links, err := b.store.LineageLinks(c)
		if err != nil {
			t.Fatal(err)
		}
		last := links[len(links)-1]
		if !last.BaseTurnKnown || !last.BaseTurnExact {
			if err := b.RecordBaseTurn(c, last.BaseLT, 2, true); err != nil {
				t.Fatal(err)
			}
			writes++
		}
	}
	if writes != 1 {
		t.Fatalf("two lineage walks over an unrecorded fork wrote .forkturn %d times, want once", writes)
	}
}

// A promote moves where a row is drawn, never where its history comes from.
func TestAPromoteAboveTheParentCutsNothingDifferently(t *testing.T) {
	b, outfit := presentBackend(t)
	p, _ := b.CreateConversation(outfit)
	writeTurns(t, b, p, 6)
	_, c, err := b.ForkAt(p, 4)
	if err != nil {
		t.Fatal(err)
	}
	writeTurns(t, b, c, 3)
	r := &lineageRig{t: t, b: b, held: map[string]ariaReadsOpen{}}
	r.hold(c)
	before, _ := b.store.Lineage(c)
	beforeReads := ariaReads{readCuts(r.held[c].ir), readCuts(r.held[c].trans)}
	if _, err := b.Promote(c, 1); err != nil {
		t.Fatal(err)
	}
	if n, _ := b.Node(p); n.Present != c {
		t.Fatalf("the promote did not draw %s under %s: %+v", p, c, n)
	}
	after, _ := b.store.Lineage(c)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("a promote moved the lineage: %v -> %v", before, after)
	}
	if got := (ariaReads{readCuts(r.held[c].ir), readCuts(r.held[c].trans)}); !reflect.DeepEqual(got, beforeReads) {
		t.Fatal("a promote changed what the child reads")
	}
	r.check("promote above the parent")
}

// C1: the rebuild runs under XwalStore.mu, now reached from treeLog.span and
// TurnCache.put. Nothing it calls may call out of the store: a callback run
// from inside it would hold XwalStore.mu while taking an aria's locks.
func TestTheSnapshotRebuildCallsNothingOutOfTheStore(t *testing.T) {
	b, outfit := presentBackend(t)
	var inside atomic.Int64
	var calls atomic.Int64
	watch := func() {
		calls.Add(1)
		buf := make([]byte, 64<<10)
		if strings.Contains(string(buf[:runtime.Stack(buf, false)]), "(*XwalStore).topologySnapshot") {
			inside.Add(1)
		}
	}
	b.SetDetachCarry(func([]string) error { watch(); return nil })
	if tt, ok := b.store.tree.(*TopologyTree); ok {
		tt.form.OnCommit(func(Commit) { watch() })
	}
	var ids []string
	p, _ := b.CreateConversation(outfit)
	ids = append(ids, p)
	writeTurns(t, b, p, 4)
	for i := 0; i < 3; i++ {
		_, c, err := b.Fork(ids[len(ids)-1])
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, c)
		writeTurns(t, b, c, 1)
	}
	for _, id := range ids {
		if err := b.WatchForm(id, func(Commit) { watch() }); err != nil {
			t.Fatal(err)
		}
	}
	steps := []func() error{
		func() error { _, err := b.Promote(ids[3], 1); return err },
		func() error { _, err := b.Demote(ids[1], ""); return err },
		func() error {
			_, err := b.ApplyForm(ids[2], form.Build(form.Snapshot{}, map[string]json.RawMessage{"x": json.RawMessage(`1`)}, nil))
			return err
		},
		func() error { return detach(t, b.store.trunks, ids[3]) },
		func() error { return b.Remove(ids[2], true) },
	}
	for i, step := range steps {
		if err := step(); err != nil {
			t.Fatalf("step %d: %v", i, err)
		}
		for _, id := range ids {
			_, _ = b.store.Lineage(id)
		}
	}
	if calls.Load() == 0 {
		t.Fatal("no callback ran: the test watches nothing")
	}
	if n := inside.Load(); n != 0 {
		t.Fatalf("%d callbacks ran from inside the snapshot rebuild", n)
	}
}

// Readers race topology moves on other arias; run under -race. A deadlock
// between XwalStore.mu and a reader's locks fails at the watchdog.
func TestReadsRaceTopologyMovesWithoutDeadlock(t *testing.T) {
	b, outfit := presentBackend(t)
	var readers []string
	for i := 0; i < 3; i++ {
		p, _ := b.CreateConversation(outfit)
		writeTurns(t, b, p, 5)
		_, c, err := b.ForkAt(p, 3)
		if err != nil {
			t.Fatal(err)
		}
		writeTurns(t, b, c, 2)
		readers = append(readers, c)
	}
	stop := make(chan struct{})
	var wg sync.WaitGroup
	for _, id := range readers {
		ir, err := b.OpenFigIR(id)
		if err != nil {
			t.Fatal(err)
		}
		tr, err := b.OpenTranslator(id, lineageProvider)
		if err != nil {
			t.Fatal(err)
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				_ = ir.ReadFrom(1, 0)
				_ = tr.Read()
				_, _ = b.store.Lineage(id)
			}
		}()
	}
	errs := make(chan error, 1)
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			p, err := b.CreateConversation(outfit)
			if err == nil {
				err = appendTurns(b, p, 2)
			}
			if err == nil {
				var c, d string
				_, c, err = b.Fork(p)
				if err == nil {
					_, d, err = b.Fork(c)
				}
				if err == nil {
					_, err = b.Promote(c, 1)
				}
				if err == nil {
					err = detach(t, b.store.trunks, d)
				}
				if err == nil {
					err = b.Move(d, p)
					if errors.Is(err, xwal.ErrMoveCopies) {
						err = nil
					}
				}
				if err == nil {
					err = b.Remove(p, true)
				}
			}
			if err != nil {
				select {
				case errs <- fmt.Errorf("mutation round %d: %w", i, err):
				default:
				}
				return
			}
		}
	}()
	time.Sleep(2 * time.Second)
	close(stop)
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		buf := make([]byte, 1<<20)
		t.Fatalf("readers and movers deadlocked:\n%s", buf[:runtime.Stack(buf, true)])
	}
	select {
	case err := <-errs:
		t.Fatal(err)
	default:
	}
	r := &lineageRig{t: t, b: b, held: map[string]ariaReadsOpen{}}
	for _, id := range readers {
		r.hold(id)
	}
	r.check("after the race")
}
