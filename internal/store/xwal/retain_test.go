package xwal

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/jack-work/figaro/internal/store/disk"
)

func retainCfg() Config {
	cfg := unkeyedCfg()
	cfg.SegmentSize = 1024
	return cfg
}

type retainFixture struct {
	t     *testing.T
	dir   string
	f     *Trunks
	form  TrunkID
	n     int
	pins  map[TrunkID]uint64
	state map[TrunkID]string
}

func newRetainFixture(t *testing.T) *retainFixture {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "f")
	f, err := createTrunks(dir, retainCfg())
	if err != nil {
		t.Fatal(err)
	}
	cleanupTrunks(t, f)
	form, err := f.SpawnUnderRootKind("form")
	if err != nil {
		t.Fatal(err)
	}
	return &retainFixture{t: t, dir: dir, f: f, form: form, pins: map[TrunkID]uint64{}, state: map[TrunkID]string{}}
}

func (fx *retainFixture) patch(count int) uint64 {
	fx.t.Helper()
	var idx uint64
	for range count {
		fx.n++
		p, _ := MapSetPatch([]string{fmt.Sprintf("k%03d", fx.n)}, fmt.Appendf(nil, `%q`, strings.Repeat("x", 200)))
		var err error
		if idx, err = fx.f.AppendChannel(string(fx.form), "chalkboard", 0, p, nil); err != nil {
			fx.t.Fatal(err)
		}
	}
	if err := fx.f.SyncChannelThrough(string(fx.form), "chalkboard", idx); err != nil {
		fx.t.Fatal(err)
	}
	return idx
}

func (fx *retainFixture) stateOf(trunk TrunkID, at uint64) (string, error) {
	x, err := fx.f.Head(string(trunk))
	if err != nil {
		return "", err
	}
	defer x.Close()
	st, err := x.StateAt("chalkboard", at)
	return string(st), err
}

func (fx *retainFixture) birth() TrunkID {
	fx.t.Helper()
	at := fx.last(fx.form)
	child, err := fx.f.SpawnChildKind(fx.form, "conversation")
	if err != nil {
		fx.t.Fatal(err)
	}
	st, err := fx.stateOf(fx.form, at)
	if err != nil {
		fx.t.Fatal(err)
	}
	fx.pins[child], fx.state[child] = at, st
	return child
}

func (fx *retainFixture) last(trunk TrunkID) uint64 {
	fx.t.Helper()
	x, err := fx.f.Head(string(trunk))
	if err != nil {
		fx.t.Fatal(err)
	}
	defer x.Close()
	_, last, _ := x.ChannelBounds("chalkboard")
	return last
}

func (fx *retainFixture) formNode() string {
	k, ok := fx.f.idx.Head(string(fx.form))
	if !ok {
		fx.t.Fatal("the form has no head")
	}
	return k
}

func (fx *retainFixture) childrenRead(when string) {
	fx.t.Helper()
	for child, at := range fx.pins {
		got, err := fx.stateOf(child, at)
		if err != nil || got != fx.state[child] {
			fx.t.Fatalf("%s: child born at %d reads %.60q, %v; want %.60q", when, at, got, err, fx.state[child])
		}
	}
}

func TestRetainDropsOnlySegmentsNoChildWasBornIn(t *testing.T) {
	fx := newRetainFixture(t)
	fx.patch(12)
	fx.birth()
	fx.patch(20)
	fx.birth()
	fx.patch(20)
	head, _ := fx.stateOf(fx.form, fx.last(fx.form))

	plan, err := fx.f.Retain(fx.formNode(), "chalkboard", true)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Dropped) == 0 {
		t.Fatal("the dry run found nothing; the fixture needs unpinned sealed segments")
	}
	if n, _ := disk.SegmentFiles(filepath.Join(fx.dir, "chalkboard", fsName(fx.formNode()))); n < minDroppableFiles+len(plan.Dropped)-1 {
		t.Fatalf("the dry run touched the disk: %d files left", n)
	}
	for _, sp := range plan.Dropped {
		for _, p := range plan.Pins {
			if p >= sp.First && p <= sp.Last {
				t.Fatalf("the plan drops [%d, %d], which holds pin %d", sp.First, sp.Last, p)
			}
		}
	}

	done, err := fx.f.Retain(fx.formNode(), "chalkboard", false)
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(done.Dropped) != fmt.Sprint(plan.Dropped) {
		t.Fatalf("dropped %v, the dry run said %v", done.Dropped, plan.Dropped)
	}
	if done.Bytes() <= 0 {
		t.Fatal("dropping segments freed no bytes")
	}

	fx.childrenRead("live")
	if got, err := fx.stateOf(fx.form, fx.last(fx.form)); err != nil || got != head {
		t.Fatalf("the form's head moved: %v", err)
	}
	x, err := fx.f.Head(string(fx.form))
	if err != nil {
		t.Fatal(err)
	}
	floor := x.ChannelFloor("chalkboard")
	_, rerr := x.ReadAt("chalkboard", done.Dropped[0].First)
	x.Close()
	if floor < done.Dropped[len(done.Dropped)-1].Last {
		t.Fatalf("floor %d sits under the dropped span ending at %d", floor, done.Dropped[len(done.Dropped)-1].Last)
	}
	if rerr == nil {
		t.Fatal("a dropped record still reads")
	}

	if again, _ := fx.f.Retain(fx.formNode(), "chalkboard", true); len(again.Dropped) != 0 {
		t.Fatalf("a second pass would drop %v", again.Dropped)
	}

	fx.f.Close()
	if fx.f, err = openTrunks(fx.dir, retainCfg()); err != nil {
		t.Fatal(err)
	}
	cleanupTrunks(t, fx.f)
	fx.childrenRead("reopened")
}

func TestRetainLeavesAFormWithoutHistoryAlone(t *testing.T) {
	fx := newRetainFixture(t)
	fx.patch(2)
	if ok, err := fx.f.MayRetain(fx.formNode(), "chalkboard"); err != nil || ok {
		t.Fatalf("MayRetain on a one-segment form = %v, %v", ok, err)
	}
	r, err := fx.f.Retain(fx.formNode(), "chalkboard", false)
	if err != nil || len(r.Dropped) != 0 {
		t.Fatalf("Retain = %+v, %v", r, err)
	}
}

// The race this defends: a birth between reading the pins and dropping
// would pin a segment the drop did not know about.
func TestRetainRacingBirthsNeverStrandsAChild(t *testing.T) {
	fx := newRetainFixture(t)
	fx.patch(10)
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	errs := make(chan error, 1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			if _, err := fx.f.Retain(fx.formNode(), "chalkboard", false); err != nil {
				select {
				case errs <- err:
				default:
				}
				return
			}
		}
	}()
	for range 30 {
		fx.patch(3)
		fx.birth()
	}
	close(stop)
	wg.Wait()
	select {
	case err := <-errs:
		t.Fatal(err)
	default:
	}
	fx.childrenRead("after the race")
}

// Removing a form detaches its children, which used to copy the form's
// history from record 1; across a dropped segment that copy must start
// from the watermark the child was born under instead.
func TestAChildDetachesFromACompactedForm(t *testing.T) {
	fx := newRetainFixture(t)
	fx.patch(30)
	child := fx.birth()
	fx.patch(10)
	if r, err := fx.f.Retain(fx.formNode(), "chalkboard", false); err != nil || len(r.Dropped) == 0 {
		t.Fatalf("Retain = %+v, %v; the test needs a drop", r, err)
	}
	if err := fx.f.Detach(fx.f.head(string(child))); err != nil {
		t.Fatalf("detach over a hole: %v", err)
	}
	fx.childrenRead("detached")
	if err := os.RemoveAll(filepath.Join(fx.dir, "chalkboard", fsName(fx.formNode()))); err != nil {
		t.Fatal(err)
	}
	fx.f.Close()
	var err error
	if fx.f, err = openTrunks(fx.dir, retainCfg()); err != nil {
		t.Fatalf("reopen with the form gone: %v", err)
	}
	cleanupTrunks(t, fx.f)
	fx.childrenRead("form gone")
	x, err := fx.f.Head(string(child))
	if err != nil {
		t.Fatal(err)
	}
	defer x.Close()
	if floor := x.ChannelFloor("chalkboard"); floor == 0 {
		t.Fatal("the detached child claims history it never copied")
	}
}

func TestAChildExportsFromACompactedForm(t *testing.T) {
	fx := newRetainFixture(t)
	fx.patch(30)
	child := fx.birth()
	fx.patch(10)
	if r, err := fx.f.Retain(fx.formNode(), "chalkboard", false); err != nil || len(r.Dropped) == 0 {
		t.Fatalf("Retain = %+v, %v; the test needs a drop", r, err)
	}
	e, err := fx.f.Export(string(child), "0123abcd")
	if err != nil {
		t.Fatalf("export over a hole: %v", err)
	}
	defer e.Close()
	dst, err := createTrunks(filepath.Join(t.TempDir(), "dst"), retainCfg())
	if err != nil {
		t.Fatal(err)
	}
	cleanupTrunks(t, dst)
	g, err := dst.BeginGraft("0123abcd", e.Component)
	if err != nil {
		t.Fatal(err)
	}
	ship(t, e, g, 4096)
	digest, _ := e.Digest()
	if err := g.Commit(digest); err != nil {
		t.Fatalf("commit: %v", err)
	}
	x, err := dst.Head(string(child))
	if err != nil {
		t.Fatal(err)
	}
	defer x.Close()
	got, err := x.StateAt("chalkboard", fx.pins[child])
	if err != nil || string(got) != fx.state[child] {
		t.Fatalf("the moved child reads %.60q, %v", got, err)
	}
}
