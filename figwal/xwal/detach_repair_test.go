package xwal

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jack-work/figaro/figwal/segment"
)

func channelRows(t *testing.T, s *Store, trunk, channel string) []string {
	t.Helper()
	recs, err := s.RecordsFrom(trunk, channel, 0, 0)
	if err != nil {
		t.Fatalf("read %s of %s: %v", channel, trunk, err)
	}
	out := make([]string, 0, len(recs))
	for _, r := range recs {
		out = append(out, fmt.Sprintf("%d:%s", r.ChannelLT, r.Payload))
	}
	return out
}

type trunkRows struct{ main, state []string }

func readRows(t *testing.T, s *Store, trunk string) trunkRows {
	t.Helper()
	return trunkRows{channelRows(t, s, trunk, "main"), channelRows(t, s, trunk, "state")}
}

func appendTurns(t *testing.T, s *Store, trunk, tag string, n int) {
	t.Helper()
	for i := range n {
		lt, err := s.Append(trunk, "main", 0, fmt.Appendf(nil, `{"%s":%d}`, tag, i), nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.Append(trunk, "state", lt, fmt.Appendf(nil, `{"%s":%d}`, tag, i), nil); err != nil {
			t.Fatal(err)
		}
	}
}

func snapshotFiles(t *testing.T, root string) map[string][]byte {
	t.Helper()
	out := map[string][]byte{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		out[rel] = b
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func putFile(t *testing.T, root, rel string, body []byte, present bool) {
	t.Helper()
	p := filepath.Join(root, rel)
	if !present {
		if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
			t.Fatal(err)
		}
		return
	}
	if err := os.WriteFile(p, body, 0o644); err != nil {
		t.Fatal(err)
	}
}

func reopen(t *testing.T, s *Store, dir string) *Store {
	t.Helper()
	if s != nil {
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
	}
	s, err := OpenStore(dir, rootDetachOptions())
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// detachFixture is a child node with its own records on both channels,
// forked from a parent with records, and the files a completed Detach of
// the child changes, before and after.
type detachFixture struct {
	dir, child, node string
	pre, post        map[string][]byte
	changed          []string
}

func newDetachFixture(t *testing.T) *detachFixture {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "s")
	s := reopen(t, nil, dir)
	parent, err := s.SpawnUnderRoot()
	if err != nil {
		t.Fatal(err)
	}
	appendTurns(t, s, parent, "p", 3)
	child, err := s.Fork(parent, 0)
	if err != nil {
		t.Fatal(err)
	}
	appendTurns(t, s, child, "c", 3)
	node, ok := s.HeadNode(child)
	if !ok {
		t.Fatalf("no head node for %s", child)
	}
	s = reopen(t, s, dir)
	pre := snapshotFiles(t, dir)
	if err := s.Detach(node); err != nil {
		t.Fatalf("detach: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	post := snapshotFiles(t, dir)
	n := fsName(node)
	changed := []string{
		filepath.Join("main", n, segFileName(1, jsonl)),
		filepath.Join("state", n, segFileName(1, jsonl)),
		filepath.Join("main", n, ".fork"),
		filepath.Join("state", n, ".fork"),
		filepath.Join("main", n, nodeMarkerName),
	}
	for rel, b := range post {
		if !strings.Contains(rel, string(filepath.Separator)+n+string(filepath.Separator)) {
			continue
		}
		if old, ok := pre[rel]; ok && bytes.Equal(old, b) {
			continue
		}
		known := false
		for _, c := range changed {
			known = known || c == rel
		}
		if !known {
			t.Fatalf("detach changed %s, which the crash images below do not cover", rel)
		}
	}
	return &detachFixture{dir: dir, child: child, node: node, pre: pre, post: post, changed: changed}
}

// crashAfter puts the store in the image a kill leaves after the first k of
// Detach's writes landed, in the order Detach makes them.
func (f *detachFixture) crashAfter(t *testing.T, k int) {
	t.Helper()
	for i, rel := range f.changed {
		img := f.pre
		if i < k {
			img = f.post
		}
		b, ok := img[rel]
		putFile(t, f.dir, rel, b, ok)
	}
}

var jsonl = func() segment.SegmentCodec {
	c, err := codecByName("jsonl")
	if err != nil {
		panic(err)
	}
	return c
}()

// A Detach killed between any two of its writes, then the store reopened
// and written to, then Detach run again: every record the child held, and
// every record written in between, is still there, and the second Detach
// completes the first.
func TestDetachInterruptedAtEachStepLosesNoRecord(t *testing.T) {
	steps := []string{
		"before any write, a half-built scratch segment left",
		"main absorbed",
		"main and state absorbed",
		"main flipped to base=1",
		"both flipped, .node not yet rewritten",
	}
	for k, name := range steps {
		t.Run(name, func(t *testing.T) {
			f := newDetachFixture(t)
			f.crashAfter(t, k)
			if k == 0 {
				half := f.post[f.changed[0]]
				putFile(t, f.dir, filepath.Join("main", fsName(f.node), ".absorb-1"), half[:len(half)/2], true)
			}
			s := reopen(t, nil, f.dir)
			want := readRows(t, s, f.child)
			if len(want.main) < 7 || len(want.state) < 7 {
				t.Fatalf("after the crash the child reads %v, want its parent's and its own records", want)
			}
			appendTurns(t, s, f.child, "after", 4)
			s = reopen(t, s, f.dir)
			got := readRows(t, s, f.child)
			want.main = append(want.main, got.main[len(want.main):]...)
			want.state = append(want.state, got.state[len(want.state):]...)
			if len(got.main) != len(want.main) || len(want.main) < 11 || len(want.state) < 11 {
				t.Fatalf("after appends the child reads %v", got)
			}
			if err := s.Detach(f.node); err != nil {
				t.Fatalf("detach re-run: %v", err)
			}
			if got := readRows(t, s, f.child); fmt.Sprint(got) != fmt.Sprint(want) {
				t.Fatalf("after the re-run the child reads\n%v\nwant\n%v", got, want)
			}
			s = reopen(t, s, f.dir)
			defer s.Close()
			if got := readRows(t, s, f.child); fmt.Sprint(got) != fmt.Sprint(want) {
				t.Fatalf("after the re-run and a reopen the child reads\n%v\nwant\n%v", got, want)
			}
			for _, ch := range []string{"main", "state"} {
				if b, _ := os.ReadFile(filepath.Join(f.dir, ch, fsName(f.node), ".fork")); string(b) != "base=1\n" {
					t.Errorf("%s .fork is %q after the re-run, want base=1", ch, b)
				}
			}
			if m, _ := readNodeMarker(filepath.Join(f.dir, "main", fsName(f.node))); m.from != "" {
				t.Errorf("the re-run left .node from=%q, want the node detached", m.from)
			}
		})
	}
}

// The repair is a .fork write and nothing else: a kill inside it leaves
// either the interrupted image with scratch files beside it, or the
// finished one. Both open with every record.
func TestTheRepairSurvivesAKillInsideIt(t *testing.T) {
	for _, flipped := range []bool{false, true} {
		t.Run(fmt.Sprintf("fork-written=%v", flipped), func(t *testing.T) {
			f := newDetachFixture(t)
			f.crashAfter(t, 2)
			s := reopen(t, nil, f.dir)
			want := readRows(t, s, f.child)
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			f.crashAfter(t, 2)
			n := fsName(f.node)
			half := f.post[f.changed[1]]
			putFile(t, f.dir, filepath.Join("state", n, ".absorb-1"), half[:len(half)/2], true)
			putFile(t, f.dir, filepath.Join("state", n, "..fork-123"), []byte("base=1\n"), true)
			if flipped {
				putFile(t, f.dir, filepath.Join("state", n, ".fork"), []byte("base=1\n"), true)
			}
			s = reopen(t, nil, f.dir)
			appendTurns(t, s, f.child, "after", 2)
			s = reopen(t, s, f.dir)
			defer s.Close()
			got := readRows(t, s, f.child)
			if len(got.state) != len(want.state)+2 || fmt.Sprint(got.state[:len(want.state)]) != fmt.Sprint(want.state) {
				t.Fatalf("after a kill inside the repair the child reads state\n%v\nwant\n%v and two more", got.state, want.state)
			}
		})
	}
}

// If the segment below the base is not what absorbing the parent's prefix
// would build, it is not this interrupted Detach, and the open refuses
// without rewriting anything.
func TestOpenRefusesASegmentBelowTheBaseThatIsNotTheParentsPrefix(t *testing.T) {
	f := newDetachFixture(t)
	f.crashAfter(t, 2)
	n := fsName(f.node)
	seg := filepath.Join(f.dir, "state", n, segFileName(1, jsonl))
	b, err := os.ReadFile(seg)
	if err != nil {
		t.Fatal(err)
	}
	tampered := bytes.Replace(b, []byte(`"p":0`), []byte(`"p":9`), 1)
	if bytes.Equal(tampered, b) {
		t.Fatalf("no parent record to tamper with in %s", b)
	}
	putFile(t, f.dir, filepath.Join("state", n, segFileName(1, jsonl)), tampered, true)
	before := snapshotFiles(t, filepath.Join(f.dir, "state", n))

	s := reopen(t, nil, f.dir)
	defer s.Close()
	_, err = s.RecordsFrom(f.child, "state", 0, 0)
	if !errors.Is(err, ErrAbsorbConflict) {
		t.Fatalf("reading a channel with a foreign segment below its base: %v, want ErrAbsorbConflict", err)
	}
	base := strings.TrimSpace(strings.TrimPrefix(string(f.pre[filepath.Join("state", n, ".fork")]), "base="))
	for _, part := range []string{filepath.Join("state", n), "base " + base, segFileName(1, jsonl)} {
		if !strings.Contains(err.Error(), part) {
			t.Errorf("the refusal %q does not name %q", err, part)
		}
	}
	lt, err := s.Append(f.child, "main", 0, []byte(`{"x":1}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Append(f.child, "state", lt, []byte(`{"x":1}`), nil); !errors.Is(err, ErrAbsorbConflict) {
		t.Errorf("append to the refused channel: %v, want ErrAbsorbConflict", err)
	}
	if err := s.Detach(f.node); !errors.Is(err, ErrAbsorbConflict) {
		t.Errorf("detach of the refused node: %v, want ErrAbsorbConflict", err)
	}
	after := snapshotFiles(t, filepath.Join(f.dir, "state", n))
	for rel, b := range before {
		if !bytes.Equal(after[rel], b) {
			t.Errorf("the refusal rewrote state/%s/%s", n, rel)
		}
	}
}

// absorbPrefix publishes without replacing: a segment already at its name
// stands if it is this very prefix and refuses otherwise.
func TestAbsorbNeverReplacesASegment(t *testing.T) {
	f := newDetachFixture(t)
	f.crashAfter(t, 0)
	n := fsName(f.node)
	absorbed := f.post[filepath.Join("state", n, segFileName(1, jsonl))]
	for _, tc := range []struct {
		name  string
		body  []byte
		valid bool
	}{
		{"the same prefix", absorbed, true},
		{"other records", append(append([]byte(nil), absorbed...), absorbed[bytes.IndexByte(absorbed, '\n')+1:]...), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := reopen(t, nil, f.dir)
			defer s.Close()
			x, err := s.Head(f.child)
			if err != nil {
				t.Fatal(err)
			}
			defer x.Close()
			ch := x.channel("state")
			l, err := ch.Log()
			if err != nil {
				t.Fatal(err)
			}
			dir := filepath.Join(f.dir, "state", n)
			seg := filepath.Join(dir, segFileName(1, jsonl))
			putFile(t, f.dir, filepath.Join("state", n, segFileName(1, jsonl)), tc.body, true)
			defer os.Remove(seg)
			err = absorbPrefix(dir, l, l.ForkBase(), ch.absorbInitial(), x.codec)
			if tc.valid && err != nil {
				t.Fatalf("absorb over the identical prefix: %v", err)
			}
			if !tc.valid && !errors.Is(err, ErrAbsorbConflict) {
				t.Fatalf("absorb over other records: %v, want ErrAbsorbConflict", err)
			}
			if b, _ := os.ReadFile(seg); !bytes.Equal(b, tc.body) {
				t.Errorf("absorb rewrote %s", seg)
			}
			if pathExists(absorbScratch(dir)) {
				t.Errorf("absorb left its scratch segment behind")
			}
		})
	}
}

// Export absorbs the top node's prefix into scratch and adds the node's own
// files; in the interrupted layout both held a 0001. Open now finishes or
// refuses that layout first, and the package never names a file twice.
func TestExportOfAnInterruptedDetach(t *testing.T) {
	t.Run("finished on open", func(t *testing.T) {
		dir, tr := interruptedRootTrunk(t)
		s := reopen(t, nil, dir)
		defer s.Close()
		e, err := s.Export(tr, "0123abcd")
		if err != nil {
			t.Fatalf("export: %v", err)
		}
		defer e.Close()
		seen := map[string]int{}
		for _, f := range e.Files {
			seen[f.Node+"/"+f.Channel+"/"+f.Name]++
		}
		for k, c := range seen {
			if c > 1 {
				t.Errorf("the package holds %s %d times", k, c)
			}
		}
	})
	t.Run("refused", func(t *testing.T) {
		dir, tr := interruptedRootTrunk(t)
		s := reopen(t, nil, dir)
		node, _ := s.HeadNode(tr)
		seg := filepath.Join(dir, "state", fsName(node), segFileName(1, jsonl))
		b, err := os.ReadFile(seg)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
		putFile(t, dir, filepath.Join("state", fsName(node), segFileName(1, jsonl)), bytes.Replace(b, []byte(`"_idx":1,`), []byte(`"_idx":1,"z":0,`), 1), true)
		s = reopen(t, nil, dir)
		defer s.Close()
		if _, err := s.Export(tr, "0123abcd"); !errors.Is(err, ErrAbsorbConflict) {
			t.Fatalf("export of a foreign segment below the base: %v, want ErrAbsorbConflict", err)
		}
	})
	t.Run("a name added twice", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, segFileName(1, jsonl)), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		e := &Export{}
		keep := func(string) bool { return true }
		if err := e.addDir("n0", "state", dir, keep); err != nil {
			t.Fatal(err)
		}
		if err := e.addDir("n0", "state", dir, keep); !errors.Is(err, ErrAbsorbConflict) {
			t.Fatalf("adding %s twice: %v, want ErrAbsorbConflict", segFileName(1, jsonl), err)
		}
	})
}

// interruptedRootTrunk is the layout TestCrashKill found: a root-level
// trunk whose state prefix was absorbed into its own 0001 under base=2,
// with records appended after it.
func interruptedRootTrunk(t *testing.T) (string, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "s")
	s := reopen(t, nil, dir)
	tr, err := s.SpawnUnderRoot()
	if err != nil {
		t.Fatal(err)
	}
	node, _ := s.HeadNode(tr)
	if err := s.Detach(node); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	putFile(t, dir, filepath.Join("state", fsName(node), ".fork"), []byte("base=2\n"), true)
	b, err := os.ReadFile(filepath.Join(dir, "state", fsName(node), segFileName(1, jsonl)))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(b, []byte(`"_idx":1,`)) {
		t.Fatalf("the absorbed state segment holds no record 1: %s", b)
	}
	return dir, tr
}

// Why TestCrashKill's n0 says .node from= and .fork base=2 at once: from=
// names the store root, whose channel directories hold record 1 (main's
// genesis, state's initial watermark). Every root-level trunk forks it at
// 2, so its first Detach absorbs record 1 into its own 0001.
func TestARootLevelNodeForksTheStoreRootAtTwo(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "s")
	s := reopen(t, nil, dir)
	defer s.Close()
	tr, err := s.SpawnUnderRoot()
	if err != nil {
		t.Fatal(err)
	}
	node, _ := s.HeadNode(tr)
	m, ok := readNodeMarker(filepath.Join(dir, "main", fsName(node)))
	if !ok || m.from != "" {
		t.Fatalf("root-level node marker %+v, want from= empty", m)
	}
	for _, ch := range []string{"main", "state"} {
		if b, _ := os.ReadFile(filepath.Join(dir, ch, fsName(node), ".fork")); string(b) != "base=2\n" {
			t.Errorf("%s .fork %q, want base=2", ch, b)
		}
		root, err := os.ReadFile(filepath.Join(dir, ch, segFileName(1, jsonl)))
		if err != nil || !bytes.Contains(root, []byte(`"_idx":1,`)) {
			t.Errorf("the store root's %s 0001 holds no record 1: %q %v", ch, root, err)
		}
		if _, err := os.Stat(filepath.Join(dir, ch, fsName(node), segFileName(1, jsonl))); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("a fresh root-level node already owns %s 0001: %v", ch, err)
		}
	}
	if rows := channelRows(t, s, tr, "state"); len(rows) != 1 || !strings.HasPrefix(rows[0], "1:") {
		t.Errorf("a fresh root-level trunk reads state %v, want the root's record 1", rows)
	}
}
