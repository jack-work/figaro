package xwal

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"sync/atomic"
	"testing"
)

var moveSeq atomic.Int64

func moveCfg(prefix string) Config {
	return Config{
		Main: "ir",
		Channels: []ChannelSpec{
			{Name: "ir", Kind: ChannelLog},
			{Name: "form", Kind: ChannelReducible, Reducer: MapReducerName, Unkeyed: true},
			{Name: "tr/anthropic", Kind: ChannelLog, Opaque: true},
		},
		SegmentSize: 512,
		MintTrunkID: func(string, func(string) bool) string {
			return fmt.Sprintf("%s%04d", prefix, moveSeq.Add(1))
		},
	}
}

func openMoveStore(t *testing.T, dir, prefix string) *Trunks {
	t.Helper()
	var f *Trunks
	var err error
	if pathExists(filepath.Join(dir, manifestName)) {
		f, err = openTrunks(dir, moveCfg(prefix))
	} else {
		f, err = createTrunks(dir, moveCfg(prefix))
	}
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func turn(t *testing.T, f *Trunks, trunk string, i int) {
	t.Helper()
	_, lt, err := f.Append(trunk, 0, fmt.Appendf(nil, `{"say":"%s-%d"}`, trunk, i), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.AppendChannel(trunk, "tr/anthropic", lt, fmt.Appendf(nil, "\x00opaque %s %d \xff", trunk, i), nil); err != nil {
		t.Fatal(err)
	}
	patch, _ := MapSetPatch([]string{"n"}, fmt.Appendf(nil, `%d`, i))
	if _, err := f.AppendChannel(trunk, "form", 0, patch, nil); err != nil {
		t.Fatal(err)
	}
}

type view struct {
	Records map[string][]string
	Form    string
	Parent  string
}

func viewOf(t *testing.T, f *Trunks, trunk string) view {
	t.Helper()
	x, err := f.Head(trunk)
	if err != nil {
		t.Fatalf("head %s: %v", trunk, err)
	}
	defer x.Close()
	v := view{Records: map[string][]string{}}
	for _, c := range x.Channels() {
		if c.Last == 0 {
			continue
		}
		err := x.chans[c.Name].mustLog().Range(1, func(idx uint64, frame []byte) error {
			v.Records[c.Name] = append(v.Records[c.Name], fmt.Sprintf("%d:%s", idx, frame))
			return nil
		})
		if err != nil {
			t.Fatalf("range %s %s: %v", trunk, c.Name, err)
		}
		if c.Name == "form" {
			st, err := x.StateAt("form", c.Last)
			if err != nil {
				t.Fatal(err)
			}
			v.Form = string(st)
		}
	}
	if p := f.node(f.head(trunk)).From; p != "" {
		v.Parent = f.node(p).Trunk
	}
	return v
}

func ship(t *testing.T, e *Export, g *Graft, chunk int) {
	t.Helper()
	for _, file := range e.Files {
		r, err := file.Open()
		if err != nil {
			t.Fatal(err)
		}
		buf := make([]byte, chunk)
		var off int64
		for {
			n, rerr := r.Read(buf)
			if n > 0 {
				if err := g.Write(file.Node, file.Channel, file.Name, off, buf[:n]); err != nil {
					t.Fatal(err)
				}
				off += int64(n)
			}
			if rerr == io.EOF {
				break
			}
			if rerr != nil {
				t.Fatal(rerr)
			}
		}
		r.Close()
		if off == 0 {
			if err := g.Write(file.Node, file.Channel, file.Name, 0, nil); err != nil {
				t.Fatal(err)
			}
		}
	}
}

type moveFixture struct {
	srcDir, dstDir string
	src, dst       *Trunks
	a, b, c, other string
	mine           string
	before         map[string]view
}

func newMoveFixture(t *testing.T) *moveFixture {
	t.Helper()
	m := &moveFixture{srcDir: filepath.Join(t.TempDir(), "src"), dstDir: filepath.Join(t.TempDir(), "dst")}
	m.src = openMoveStore(t, m.srcDir, "a")
	m.dst = openMoveStore(t, m.dstDir, "b")
	t.Cleanup(func() { m.src.Close(); m.dst.Close() })
	if err := m.src.CreateStump("s"); err != nil {
		t.Fatal(err)
	}
	sx, err := m.src.StumpHead("s")
	if err != nil {
		t.Fatal(err)
	}
	birth, err := sx.AppendMain([]byte(`{"birth":"loadout s"}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sx.Append("tr/anthropic", birth, []byte("\x01loadout bytes"), nil); err != nil {
		t.Fatal(err)
	}
	patch, _ := MapSetPatch([]string{"credo"}, []byte(`"figaro"`))
	if _, err := sx.Append("form", 0, patch, nil); err != nil {
		t.Fatal(err)
	}
	if err := sx.SyncCoherent(); err != nil {
		t.Fatal(err)
	}
	sx.Close()
	m.a, err = m.src.SpawnUnderStump("s")
	if err != nil {
		t.Fatal(err)
	}
	for i := range 12 {
		turn(t, m.src, m.a, i)
	}
	m.b, err = m.src.ForkAt(m.a, 6)
	if err != nil {
		t.Fatal(err)
	}
	for i := range 9 {
		turn(t, m.src, m.b, 100+i)
	}
	m.c, err = m.src.ForkTail(m.b)
	if err != nil {
		t.Fatal(err)
	}
	turn(t, m.src, m.c, 200)
	m.other, err = m.src.SpawnUnderStump("s")
	if err != nil {
		t.Fatal(err)
	}
	turn(t, m.src, m.other, 300)
	if err := m.dst.CreateStump("s"); err != nil {
		t.Fatal(err)
	}
	m.mine, err = m.dst.SpawnUnderStump("s")
	if err != nil {
		t.Fatal(err)
	}
	turn(t, m.dst, m.mine, 1)
	m.src.Close()
	m.dst.Close()
	m.src = openMoveStore(t, m.srcDir, "a")
	m.dst = openMoveStore(t, m.dstDir, "b")
	m.before = map[string]view{}
	for _, id := range []string{m.a, m.b, m.c, m.other} {
		m.before[id] = viewOf(t, m.src, id)
	}
	m.before[m.mine] = viewOf(t, m.dst, m.mine)
	return m
}

func (m *moveFixture) export(t *testing.T, of string) *Export {
	t.Helper()
	e, err := m.src.Export(of, "0123abcd")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.Close() })
	return e
}

// The fork component (parent chain to the stump, and every fork below) lands
// on the other store with the same ids, the same frames byte for byte, the
// same reducible state and the same parent links; the stump stays home.
func TestAComponentMovesWholeAndReadsTheSame(t *testing.T) {
	m := newMoveFixture(t)
	e := m.export(t, m.c)
	got := e.TrunkIDs()
	sort.Strings(got)
	want := []string{m.a, m.b, m.c}
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("component %v, want %v", got, want)
	}
	digest, err := e.Digest()
	if err != nil {
		t.Fatal(err)
	}
	g, err := m.dst.BeginGraft("0123abcd", e.Component)
	if err != nil {
		t.Fatal(err)
	}
	ship(t, e, g, 97)
	if err := g.Commit(digest); err != nil {
		t.Fatal(err)
	}
	if left := m.dst.PendingGrafts(); len(left) != 0 {
		t.Fatalf("staging left behind: %v", left)
	}
	check := func(label string, dst *Trunks) {
		for _, id := range []string{m.a, m.b, m.c} {
			v := viewOf(t, dst, id)
			want := m.before[id]
			if id == m.a {
				want.Parent = ""
			}
			if !reflect.DeepEqual(v, want) {
				t.Fatalf("%s: %s reads\n%+v\nwant\n%+v", label, id, v, want)
			}
		}
		if v := viewOf(t, dst, m.mine); !reflect.DeepEqual(v, m.before[m.mine]) {
			t.Fatalf("%s: the new home's own trunk changed:\n%+v\n%+v", label, v, m.before[m.mine])
		}
	}
	check("live", m.dst)
	m.dst.Close()
	m.dst = openMoveStore(t, m.dstDir, "b")
	check("reopened", m.dst)
	turn(t, m.dst, m.c, 400)
	if _, err := m.dst.ForkAt(m.a, 3); err != nil {
		t.Fatalf("fork a moved trunk: %v", err)
	}

	removed, err := m.src.RemoveComponent(e.Component, "0123abcd")
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(removed)
	if !reflect.DeepEqual(removed, want) {
		t.Fatalf("removed %v, want %v", removed, want)
	}
	for _, id := range want {
		if _, err := m.src.Head(id); !errors.Is(err, ErrUnknownTrunk) {
			t.Fatalf("%s still on the old home: %v", id, err)
		}
	}
	if v := viewOf(t, m.src, m.other); !reflect.DeepEqual(v, m.before[m.other]) {
		t.Fatalf("a trunk outside the component changed on the old home")
	}
	for _, n := range e.Nodes {
		for _, ch := range []string{"ir", "form", "tr/anthropic"} {
			if pathExists(filepath.Join(m.srcDir, ch, n.Key)) {
				t.Fatalf("%s/%s left on the old home", ch, n.Key)
			}
		}
	}
}

// Naming any member exports the same component.
func TestEveryMemberNamesTheSameComponent(t *testing.T) {
	m := newMoveFixture(t)
	var first []string
	for _, id := range []string{m.a, m.b, m.c} {
		c, err := m.src.Component(id)
		if err != nil {
			t.Fatal(err)
		}
		if first == nil {
			first = c.TrunkIDs()
		} else if !reflect.DeepEqual(c.TrunkIDs(), first) {
			t.Fatalf("component of %s is %v, of %s %v", id, c.TrunkIDs(), m.a, first)
		}
	}
	if c, _ := m.src.Component(m.other); len(c.Nodes) != 1 {
		t.Fatalf("a sibling under the same stump joined the component: %v", c.TrunkIDs())
	}
}

// An id already living on the new home refuses the whole move before
// anything is staged.
func TestACollidingIDRefusesTheMove(t *testing.T) {
	m := newMoveFixture(t)
	e := m.export(t, m.a)
	e.Nodes[1].Trunk = m.mine
	if _, err := m.dst.BeginGraft("0123abcd", e.Component); !errors.Is(err, ErrTrunkExists) {
		t.Fatalf("begin = %v, want ErrTrunkExists", err)
	}
	if left := m.dst.PendingGrafts(); len(left) != 0 {
		t.Fatalf("a refused move staged %v", left)
	}
}

// A torn transfer is refused at commit and can be abandoned without trace.
func TestAWrongDigestRefusesAndAbortLeavesNothing(t *testing.T) {
	m := newMoveFixture(t)
	e := m.export(t, m.a)
	g, err := m.dst.BeginGraft("0123abcd", e.Component)
	if err != nil {
		t.Fatal(err)
	}
	ship(t, e, g, 4096)
	if err := g.Commit("00"); err == nil {
		t.Fatal("commit accepted a wrong digest")
	}
	if err := g.Abort(); err != nil {
		t.Fatal(err)
	}
	if left := m.dst.PendingGrafts(); len(left) != 0 {
		t.Fatalf("abort left %v", left)
	}
	if _, err := m.dst.Head(m.a); !errors.Is(err, ErrUnknownTrunk) {
		t.Fatalf("an aborted move is visible: %v", err)
	}
}

// A crash after the plan is sealed finishes on the next open, whatever was
// already renamed.
func TestASealedGraftFinishesOnReopen(t *testing.T) {
	m := newMoveFixture(t)
	e := m.export(t, m.a)
	digest, _ := e.Digest()
	g, err := m.dst.BeginGraft("0123abcd", e.Component)
	if err != nil {
		t.Fatal(err)
	}
	ship(t, e, g, 4096)
	if got, _ := g.Digest(); got != digest {
		t.Fatalf("staged digest %s, sent %s", got, digest)
	}
	if err := g.mintKeys(); err != nil {
		t.Fatal(err)
	}
	if err := g.writePlan(); err != nil {
		t.Fatal(err)
	}
	if err := g.place("form", e.Nodes[0].Key, g.plan.Keys[e.Nodes[0].Key], map[string]bool{}); err != nil {
		t.Fatal(err)
	}
	m.dst.Close()
	m.dst = openMoveStore(t, m.dstDir, "b")
	for _, id := range []string{m.a, m.b, m.c} {
		v := viewOf(t, m.dst, id)
		want := m.before[id]
		if id == m.a {
			want.Parent = ""
		}
		if !reflect.DeepEqual(v, want) {
			t.Fatalf("after resume %s reads %+v, want %+v", id, v, want)
		}
	}
	if left := m.dst.PendingGrafts(); len(left) != 0 {
		t.Fatalf("resume left %v", left)
	}
}

// An unsealed staging area is invisible and survives a restart untouched, so
// the sender can still commit or abort it.
func TestAnUnsealedGraftStaysStagedAcrossARestart(t *testing.T) {
	m := newMoveFixture(t)
	e := m.export(t, m.a)
	digest, _ := e.Digest()
	g, err := m.dst.BeginGraft("0123abcd", e.Component)
	if err != nil {
		t.Fatal(err)
	}
	ship(t, e, g, 4096)
	m.dst.Close()
	m.dst = openMoveStore(t, m.dstDir, "b")
	if _, err := m.dst.Head(m.a); !errors.Is(err, ErrUnknownTrunk) {
		t.Fatalf("an unsealed move is visible after restart: %v", err)
	}
	g, err = m.dst.OpenGraft("0123abcd")
	if err != nil {
		t.Fatal(err)
	}
	if err := g.Commit(digest); err != nil {
		t.Fatal(err)
	}
	if _, err := m.dst.Head(m.a); err != nil {
		t.Fatal(err)
	}
}

// A removal interrupted after its plan was written completes on reopen, and
// the half-removed nodes never read back as trunks.
func TestAnInterruptedRemovalCompletesOnReopen(t *testing.T) {
	m := newMoveFixture(t)
	e := m.export(t, m.a)
	c := e.Component
	plan := filepath.Join(m.srcDir, removalPrefix+"0123abcd")
	if err := os.MkdirAll(plan, 0o755); err != nil {
		t.Fatal(err)
	}
	body := fmt.Appendf(nil, `{"codec":%q,"main":"ir","nodes":[`, c.Codec)
	for i, n := range c.Nodes {
		if i > 0 {
			body = append(body, ',')
		}
		body = fmt.Appendf(body, `{"key":%q,"kind":"conversation","trunk":%q}`, n.Key, n.Trunk)
	}
	body = append(body, "]}"...)
	if err := os.WriteFile(filepath.Join(plan, graftPlanName), body, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(m.srcDir, "ir", c.Nodes[0].Key, nodeMarkerName)); err != nil {
		t.Fatal(err)
	}
	e.Close()
	m.src.Close()
	m.src = openMoveStore(t, m.srcDir, "a")
	for _, n := range c.Nodes {
		if _, err := m.src.Head(n.Trunk); !errors.Is(err, ErrUnknownTrunk) {
			t.Fatalf("%s survived an interrupted removal: %v", n.Trunk, err)
		}
		for _, ch := range []string{"ir", "form", "tr/anthropic"} {
			if pathExists(filepath.Join(m.srcDir, ch, n.Key)) {
				t.Fatalf("%s/%s left behind", ch, n.Key)
			}
		}
	}
	if v := viewOf(t, m.src, m.other); !reflect.DeepEqual(v, m.before[m.other]) {
		t.Fatal("a trunk outside the component changed")
	}
}

// Stores older than a reducer rename name the form's reducer by its old
// name; the registry still resolves it through the channel name, so a move
// between the two is between equal channels.
func TestAMoveCrossesAReducerRename(t *testing.T) {
	cfg := func(prefix, reducer string) Config {
		c := moveCfg(prefix)
		c.Channels[1].Reducer = reducer
		c.Registry = map[string]Reducer{"form": builtinReducers()[MapReducerName], "lastwins": {Reduce: func(_, p []byte) ([]byte, error) { return p, nil }, Initial: []byte("{}")}}
		return c
	}
	src, err := createTrunks(filepath.Join(t.TempDir(), "src"), cfg("o", "jsonmerge"))
	if err != nil {
		t.Fatal(err)
	}
	cleanupTrunks(t, src)
	dst, err := createTrunks(filepath.Join(t.TempDir(), "dst"), cfg("n", "form"))
	if err != nil {
		t.Fatal(err)
	}
	cleanupTrunks(t, dst)
	id, err := src.SpawnUnderRoot()
	if err != nil {
		t.Fatal(err)
	}
	turn(t, src, id, 1)
	e, err := src.Export(id, "0123abcd")
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	g, err := dst.BeginGraft("0123abcd", e.Component)
	if err != nil {
		t.Fatalf("a renamed reducer refused the move: %v", err)
	}
	ship(t, e, g, 4096)
	digest, _ := e.Digest()
	if err := g.Commit(digest); err != nil {
		t.Fatalf("a renamed reducer refused the commit: %v", err)
	}
	if v := viewOf(t, dst, id); v.Form != `{"n":1}` {
		t.Fatalf("the moved form reads %s", v.Form)
	}
	other := e.Component
	other.Channels = append([]ComponentChannel(nil), e.Channels...)
	other.Channels[1].Reducer = "lastwins"
	other.Nodes = []ComponentNode{{Key: "n1", Kind: "conversation", Trunk: "fresh"}}
	if _, err := dst.BeginGraft("0123abce", other); err == nil {
		t.Fatal("a reducer that resolves elsewhere was taken for the same channel")
	}
}
