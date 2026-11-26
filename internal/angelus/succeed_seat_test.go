package angelus

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/jack-work/figaro/api/message"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/hierarchy"
	"github.com/jack-work/figaro/internal/store"
)

func roleFor(t *testing.T, hs *Handlers, target string) string {
	t.Helper()
	raw, _ := json.Marshal(target)
	patch := form.Build(form.Snapshot{}, map[string]json.RawMessage{"name": json.RawMessage(`"seat"`), "target-aria": raw}, nil)
	req, _ := json.Marshal(rpc.FormCreateRequest{Patch: &patch})
	out, err := hs.Map[rpc.MethodFormCreate](inProcess(context.Background()), req)
	if err != nil {
		t.Fatal(err)
	}
	return out.(rpc.FormCreateResponse).FormID
}

func succeedReq(source, role string) rpc.ForkRequest {
	return rpc.ForkRequest{FigaroID: source, Seat: role, Abdicates: true, RetireTTL: 10 * time.Minute}
}

type boardOf struct {
	form  string
	nodes int
	tree  string
}

func boardsOf(t *testing.T, f seatFixture, ids ...string) []boardOf {
	t.Helper()
	var out []boardOf
	for _, id := range ids {
		snap, err := f.b.FormState(id)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(snap)
		out = append(out, boardOf{form: string(raw), nodes: len(f.b.Nodes()), tree: string(f.overrides(t))})
	}
	return out
}

func requireUntouched(t *testing.T, f seatFixture, before []boardOf, ids ...string) {
	t.Helper()
	after := boardsOf(t, f, ids...)
	for i := range before {
		if before[i] != after[i] {
			t.Fatalf("a refused succession changed %s:\nbefore %+v\nafter  %+v", ids[i], before[i], after[i])
		}
	}
}

func TestASuccessionForksSeatsAndRetiresInOneCall(t *testing.T) {
	f := newSeatFixture(t)
	applyKeys(t, f.b, f.pred, map[string]any{store.SystemTTLKey: "48h"})
	role := roleFor(t, f.hs, f.pred)
	out, err := callForkAsFigaro(t, f.hs, f.pred, succeedReq(f.pred, role))
	if err != nil {
		t.Fatal(err)
	}
	succ := out.Alternative
	if !out.Retired || out.Seated == nil || out.Seated.FailedAt != "" {
		t.Fatalf("response %+v: the call did not report a seat and a retire", out)
	}
	for id, want := range map[string]string{succ: f.top, f.pred: succ, f.kid: succ} {
		if got := f.drawn(id); got != want {
			t.Fatalf("%s is drawn under %q, want %s", id, got, want)
		}
	}
	if snap, _ := f.b.FormState(f.pred); !store.RetiredIn(snap) {
		t.Fatal("the source is not retired")
	}
	snap, _ := f.b.FormState(succ)
	if ttl, ok := snap.Get(store.SystemTTLKey); !ok || string(ttl) != `"48h"` {
		t.Fatalf("the successor's system.ttl is %s, want the source's own 48h", ttl)
	}
}

func TestASuccessionTakesTheLifetimeOfASourceThatHadNone(t *testing.T) {
	f := newSeatFixture(t)
	role := roleFor(t, f.hs, f.pred)
	out, err := callForkAsFigaro(t, f.hs, f.pred, succeedReq(f.pred, role))
	if err != nil {
		t.Fatal(err)
	}
	snap, _ := f.b.FormState(out.Alternative)
	if ttl, ok := snap.Get(store.SystemTTLKey); !ok || string(ttl) != `"0"` {
		t.Fatalf("the successor's system.ttl is %s, want 0: it must not inherit the bounded ttl the source was retired with", ttl)
	}
}

func TestAFigaroThatSucceedsFiveTimesIsAdmittedOnce(t *testing.T) {
	f := newSeatFixture(t)
	setMaxChildren(t, f.b, f.pred, 1)
	role := roleFor(t, f.hs, f.pred)
	before := len(f.b.Nodes())
	admitted := 0
	for range 5 {
		if _, err := callForkAsFigaro(t, f.hs, f.pred, succeedReq(f.pred, role)); err == nil {
			admitted++
		}
	}
	if admitted != 1 {
		t.Fatalf("%d successions of one source were admitted, want 1", admitted)
	}
	if got := len(f.b.Nodes()) - before; got != 1 {
		t.Fatalf("%d nodes were born, want 1", got)
	}
}

func TestConcurrentSuccessionsOfOneSourceAdmitOne(t *testing.T) {
	f := newSeatFixture(t)
	role := roleFor(t, f.hs, f.pred)
	before := len(f.b.Nodes())
	var wg sync.WaitGroup
	var mu sync.Mutex
	admitted := 0
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := callForkAsFigaro(t, f.hs, f.pred, succeedReq(f.pred, role)); err == nil {
				mu.Lock()
				admitted++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if admitted != 1 {
		t.Fatalf("%d concurrent successions were admitted, want 1", admitted)
	}
	if got := len(f.b.Nodes()) - before; got != 1 {
		t.Fatalf("%d nodes were born, want 1", got)
	}
}

func TestASourceOverItsCapAndUnderAParentAtItsCapIsSucceeded(t *testing.T) {
	f := newSeatFixture(t)
	for range 3 {
		if _, _, err := f.b.Fork(f.pred); err != nil {
			t.Fatal(err)
		}
	}
	setMaxChildren(t, f.b, f.pred, 1)
	setMaxChildren(t, f.b, f.top, 1)
	role := roleFor(t, f.hs, f.pred)
	out, err := callForkAsFigaro(t, f.hs, f.pred, succeedReq(f.pred, role))
	if err != nil {
		t.Fatalf("a source over its cap was refused its succession: %v", err)
	}
	if got := f.drawn(out.Alternative); got != f.top {
		t.Fatalf("the successor is drawn under %q, want %s", got, f.top)
	}
	if got := f.drawn(f.kid); got != out.Alternative {
		t.Fatalf("the inherited child is drawn under %q, want the successor", got)
	}
}

func TestASuccessionThatDoesNotRetireItsSourceIsChargedTheCap(t *testing.T) {
	f := newSeatFixture(t)
	setMaxChildren(t, f.b, f.pred, 1)
	role := roleFor(t, f.hs, f.pred)
	if _, err := callForkAsFigaro(t, f.hs, f.pred, rpc.ForkRequest{FigaroID: f.pred, Seat: role}); err == nil {
		t.Fatal("a seat fork that keeps its source was admitted over max_children")
	}
}

func TestASuccessionNeverLengthensTheLifetimeOfASourceWithLessLeftThanTheRetireTTL(t *testing.T) {
	f := newSeatFixture(t)
	applyKeys(t, f.b, f.pred, map[string]any{store.SystemTTLKey: "5m"})
	role := roleFor(t, f.hs, f.pred)
	out, err := callForkAsFigaro(t, f.hs, f.pred, succeedReq(f.pred, role))
	if err != nil {
		t.Fatalf("a source with 5m to live was refused a 10m retire: %v", err)
	}
	if !out.Retired {
		t.Fatalf("response %+v: the source was not retired", out)
	}
	snap, _ := f.b.FormState(f.pred)
	raw, _ := snap.Get(store.SystemTTLKey)
	got, err := store.ParseTTL(lifetimeText(raw))
	if err != nil || got <= 0 || got > 5*time.Minute {
		t.Fatalf("the source's system.ttl is %s, want no more than the 5m it had", raw)
	}
}

func TestAnAncestorSucceedsItsDescendantAndAStrangerDoesNot(t *testing.T) {
	f := newSeatFixture(t)
	role := roleFor(t, f.hs, f.pred)
	if _, err := callForkAsFigaro(t, f.hs, f.other, succeedReq(f.pred, role)); err == nil {
		t.Fatal("a stranger succeeded another figaro")
	}
	out, err := callForkAsFigaro(t, f.hs, f.top, succeedReq(f.pred, role))
	if err != nil {
		t.Fatalf("an ancestor was refused the succession of its child: %v", err)
	}
	if !out.Retired || out.Seated == nil {
		t.Fatalf("response %+v: the ancestor's succession did not seat and retire", out)
	}
	if snap, _ := f.b.FormState(f.pred); !store.RetiredIn(snap) {
		t.Fatal("the source is not retired")
	}
}

func TestAnOldCLIRequestWithoutARetireTTLTakesTheOrdinaryPath(t *testing.T) {
	f := newSeatFixture(t)
	role := roleFor(t, f.hs, f.pred)
	old := rpc.ForkRequest{FigaroID: f.pred, Seat: role, Abdicates: true}

	out, err := callForkAsFigaro(t, f.hs, f.pred, old)
	if err != nil {
		t.Fatalf("an old CLI's seat fork was refused: %v", err)
	}
	if out.Retired || out.Seated != nil {
		t.Fatalf("response %+v: the ordinary path neither seats nor retires", out)
	}
	if !f.hs.h.angelus.isLiveAria(f.pred) {
		t.Fatal("the ordinary path retired the source")
	}

	setMaxChildren(t, f.b, f.pred, 1)
	_, err = callForkAsFigaro(t, f.hs, f.pred, old)
	if err == nil || !strings.Contains(err.Error(), "max_children") {
		t.Fatalf("an old CLI's request past the cap must meet the ordinary limit, got %v", err)
	}
}

func TestARefusedPrecheckLeavesNothing(t *testing.T) {
	retired := func(t *testing.T, f seatFixture) { retireAria(t, f.b, f.pred) }
	cases := map[string]struct {
		prep   func(*testing.T, seatFixture)
		caller string
		mut    func(*rpc.ForkRequest)
	}{
		"a retired source":              {prep: retired, caller: "pred"},
		"a stranger":                    {caller: "other"},
		"a ttl over the hour":           {caller: "pred", mut: func(r *rpc.ForkRequest) { r.RetireTTL = 2 * time.Hour }},
		"no seat":                       {caller: "pred", mut: func(r *rpc.ForkRequest) { r.Seat = "" }},
		"the owner of a retired source": {prep: retired},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			f := newSeatFixture(t)
			role := roleFor(t, f.hs, f.pred)
			if tc.prep != nil {
				tc.prep(t, f)
			}
			ids := []string{f.pred, f.top, f.kid, f.other}
			before := boardsOf(t, f, ids...)
			req := succeedReq(f.pred, role)
			if tc.mut != nil {
				tc.mut(&req)
			}
			var err error
			switch tc.caller {
			case "":
				_, err = callFork(t, f.hs, req)
			case "pred":
				_, err = callForkAsFigaro(t, f.hs, f.pred, req)
			case "other":
				_, err = callForkAsFigaro(t, f.hs, f.other, req)
			}
			if err == nil {
				t.Fatal("the precheck admitted the succession")
			}
			requireUntouched(t, f, before, ids...)
		})
	}
}

type failedWrite struct{ hierarchy.Engine }

func (failedWrite) WriteDrawn(map[string]string, []string) error {
	return errors.New("injected: the seat")
}

type noDraws struct{ hierarchy.Engine }

func (noDraws) Draws() bool { return false }

func TestASuccessionWithoutTrunksIsRefusedAndNothingIsMade(t *testing.T) {
	f := newSeatFixture(t)
	role := roleFor(t, f.hs, f.pred)
	a := f.hs.h.angelus
	a.Topology = hierarchy.New(noDraws{f.b.HierarchyEngine()}, a.DefaultTopologyGuard())
	before := len(f.b.Nodes())
	_, err := callForkAsFigaro(t, f.hs, f.pred, succeedReq(f.pred, role))
	if err == nil || !strings.Contains(err.Error(), rpc.AbdicationNeedsTrunks) {
		t.Fatalf("a succession with no trunks: %v", err)
	}
	if len(f.b.Nodes()) != before {
		t.Fatal("a refused succession left a fork")
	}
	if _, err := callForkAsFigaro(t, f.hs, f.pred, rpc.ForkRequest{FigaroID: f.pred, Seat: role}); err != nil {
		t.Fatalf("the ordinary path with no trunks: %v", err)
	}
}

type faultyBackend struct {
	store.Backend
	fail func(id string, patch string) error
}

func (b faultyBackend) ApplyForm(id string, patch message.Patch) (uint64, error) {
	if err := b.check(id, patch); err != nil {
		return 0, err
	}
	return b.Backend.ApplyForm(id, patch)
}

func (b faultyBackend) ApplyFormPrivileged(id string, patch message.Patch) (uint64, error) {
	if err := b.check(id, patch); err != nil {
		return 0, err
	}
	return b.Backend.ApplyFormPrivileged(id, patch)
}

func (b faultyBackend) ApplyFormWrite(id string, w store.FormWrite) (store.FormVerdict, error) {
	if err := b.check(id, w.Patch); err != nil {
		return store.FormVerdict{}, err
	}
	return b.Backend.ApplyFormWrite(id, w)
}

func (b faultyBackend) check(id string, patch message.Patch) error {
	if b.fail == nil {
		return nil
	}
	raw, _ := json.Marshal(patch)
	return b.fail(id, string(raw))
}

func TestAFailureAfterTheForkOrAfterTheSeatLeavesNoForkAndAnUntouchedSource(t *testing.T) {
	stages := map[string]func(f seatFixture) faultyBackend{
		"after the fork": func(f seatFixture) faultyBackend {
			return faultyBackend{}
		},
		"after the seat": func(f seatFixture) faultyBackend {
			return faultyBackend{fail: func(id, patch string) error {
				if id == f.pred && strings.Contains(patch, `"retired"`) {
					return errors.New("injected: the retire")
				}
				return nil
			}}
		},
	}
	for name, stage := range stages {
		t.Run(name, func(t *testing.T) {
			f := newSeatFixture(t)
			setMaxChildren(t, f.b, f.pred, 3)
			applyKeys(t, f.b, f.pred, map[string]any{form.FanoutMintKey: true})
			role := roleFor(t, f.hs, f.pred)
			ids := []string{f.pred, f.top, f.kid}
			before := boardsOf(t, f, ids...)
			a := f.hs.h.angelus
			faulty := stage(f)
			faulty.Backend = f.b
			a.Backend = faulty
			if name == "after the fork" {
				a.Topology = hierarchy.New(failedWrite{f.b.HierarchyEngine()}, a.DefaultTopologyGuard())
			}
			_, err := callForkAsFigaro(t, f.hs, f.pred, succeedReq(f.pred, role))
			if err == nil || !strings.Contains(err.Error(), "injected") {
				t.Fatalf("the injected failure did not surface: %v", err)
			}
			requireUntouched(t, f, before, ids...)
			a.Backend = f.b
			a.Topology = hierarchy.New(f.b.HierarchyEngine(), a.DefaultTopologyGuard())
			if _, err := callForkAsFigaro(t, f.hs, f.pred, succeedReq(f.pred, role)); err != nil {
				t.Fatalf("the source could not succeed after a refused succession: %v", err)
			}
		})
	}
}

func TestAFailedRetireGivesTheSeatBackToASourceOverItsCap(t *testing.T) {
	f := newSeatFixture(t)
	for range 3 {
		if _, _, err := f.b.Fork(f.pred); err != nil {
			t.Fatal(err)
		}
	}
	setMaxChildren(t, f.b, f.pred, 1)
	setMaxChildren(t, f.b, f.top, 1)
	role := roleFor(t, f.hs, f.pred)
	ids := []string{f.pred, f.top, f.kid}
	before := boardsOf(t, f, ids...)
	a := f.hs.h.angelus
	a.Backend = faultyBackend{Backend: f.b, fail: func(id, patch string) error {
		if id == f.pred && strings.Contains(patch, `"retired"`) {
			return errors.New("injected: the retire")
		}
		return nil
	}}
	if _, err := callForkAsFigaro(t, f.hs, f.pred, succeedReq(f.pred, role)); err == nil || !strings.Contains(err.Error(), "injected") {
		t.Fatalf("the injected failure did not surface: %v", err)
	}
	requireUntouched(t, f, before, ids...)
}

func TestASuccessionForkNeverOutlivesALiveSource(t *testing.T) {
	f := newSeatFixture(t)
	role := roleFor(t, f.hs, f.pred)
	a := f.hs.h.angelus
	known := map[string]bool{}
	for _, n := range f.b.Nodes() {
		known[n.ID] = true
	}
	liveForksOf := func() int {
		n := 0
		for _, node := range f.b.Nodes() {
			if !known[node.ID] && a.isLiveAria(node.ID) {
				n++
			}
		}
		return n
	}
	fail := true
	a.Backend = faultyBackend{Backend: f.b, fail: func(id, patch string) error {
		if fail && id == f.pred && strings.Contains(patch, `"retired"`) {
			return errors.New("injected: the retire")
		}
		return nil
	}}
	if _, err := callForkAsFigaro(t, f.hs, f.pred, succeedReq(f.pred, role)); err == nil {
		t.Fatal("the injected failure did not surface")
	}
	if n := liveForksOf(); n != 0 || !a.isLiveAria(f.pred) {
		t.Fatalf("after a failed succession %d fork(s) of the source live beside it", n)
	}
	fail = false
	if _, err := callForkAsFigaro(t, f.hs, f.pred, succeedReq(f.pred, role)); err != nil {
		t.Fatal(err)
	}
	if n := liveForksOf(); n != 1 || a.isLiveAria(f.pred) {
		t.Fatalf("after a succession %d successor(s) live and the source live=%v", n, a.isLiveAria(f.pred))
	}
}
