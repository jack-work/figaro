package cli

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/rpc"
)

var ariasMountHint = func() *rpc.FormView {
	h := *ariasLikeHint
	h.Mount = "id"
	return &h
}()

func figaroForm(mantra string) *fakeFormSource {
	return things(
		"aria_id", `"x"`, "mantra", `"`+mantra+`"`,
		"system.model", `"claude"`, "system.cwd", `"/w"`,
		"plan.next", `"write the test"`,
	)
}

func expandRig(attended string) (*fakeMounts, *formView) {
	f := newFakeMounts()
	f.hint = ariasMountHint
	f.plain = map[string]bool{}
	f.forms["@things"] = ariasLike()
	f.forms["far/@things"] = ariasLike()
	for id, mantra := range map[string]string{"aaa": "top", "bbb": "mid", "ccc": "me", "ddd": "other"} {
		f.forms[id], f.plain[id] = figaroForm(mantra), true
		f.forms["far/"+id], f.plain["far/"+id] = figaroForm(mantra), true
	}
	v := indexView(f)
	v.subject = attending(attended)
	seekAttended(v, f.open)
	v.seekMarked()
	return f, v
}

func drawnIDs(v *formView) []string {
	var ids []string
	for _, r := range v.Items(100) {
		ids = append(ids, r.id)
	}
	return ids
}

func opened(f *fakeMounts) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.opened)
}

// l opened the attended figaro only through to its forks, so one with no
// forks looked unopened and its form was nowhere. It now opens whole wherever
// it sits: its forks, then its own form, folded as :state folds it. The
// figaros above it open only far enough to reach it, and mount nothing.
func TestLsOpensTheAttendedFigaroWholeAtAnyDepth(t *testing.T) {
	for _, tc := range []struct {
		attended, row string
		above         []string
	}{
		{"ddd", "arias.other", nil},
		{"bbb", "arias.top.forks.mid", []string{"aaa"}},
		{"ccc", "arias.top.forks.mid.forks.me", []string{"aaa", "bbb"}},
	} {
		t.Run(tc.attended, func(t *testing.T) {
			f, v := expandRig(tc.attended)
			at := "here" + mountSep + tc.row
			mounted := at + mountSep
			ids := drawnIDs(v)
			if !slices.Contains(ids, at) {
				t.Fatalf("the attended row %s is not drawn: %v", at, ids)
			}
			for _, key := range []string{"mantra", "plan", "system"} {
				if !slices.Contains(ids, mounted+key) {
					t.Errorf("the attended figaro's %s is not drawn under it: %v", key, ids)
				}
			}
			if slices.Contains(ids, mounted+"system.model") {
				t.Errorf("system is drawn open; want it folded as :state draws it")
			}
			if slices.Contains(ids, at+".id") || slices.Contains(ids, at+".mantra") {
				t.Errorf("the row's own listing leaves are drawn: %v", ids)
			}
			got := opened(f)
			if !slices.Contains(got, tc.attended) {
				t.Errorf("opened %v, want the attended figaro's form %s", got, tc.attended)
			}
			for _, id := range tc.above {
				if slices.Contains(got, id) {
					t.Errorf("an ancestor opened to reach the row mounted its form %s: %v", id, got)
				}
			}
		})
	}
}

// The attended figaro's forks stay under it beside its form.
func TestTheAttendedFigaroKeepsItsForksBesideItsForm(t *testing.T) {
	_, v := expandRig("bbb")
	ids := drawnIDs(v)
	me := "here" + mountSep + "arias.top.forks.mid"
	fork, form := slices.Index(ids, me+".forks.me"), slices.Index(ids, me+mountSep+"mantra")
	if fork < 0 || form < 0 || fork > form {
		t.Fatalf("want the fork, then the form, under %s: %v", me, ids)
	}
}

// A figaro on another node mounts there: its row names a bare id, which is
// read through that node's door.
func TestAnExpandedFigaroOnAPeerMountsThroughThePeer(t *testing.T) {
	f, v := expandRig("none")
	v.Items(100)
	v.Activate("far")
	v.Items(100)
	f.waitOpened(t, 1)
	row := "far" + mountSep + "arias.other"
	v.Items(100)
	v.Expand(row)
	v.Items(100)
	deadline := time.Now().Add(5 * time.Second)
	for !slices.Contains(opened(f), "far/ddd") {
		if time.Now().After(deadline) {
			t.Fatalf("opened %v, want far/ddd", opened(f))
		}
		v.Items(100)
		time.Sleep(10 * time.Millisecond)
	}
	if slices.Contains(opened(f), "ddd") {
		t.Errorf("a peer's figaro was read from this node: %v", opened(f))
	}
}

// An ancestor opened only to its forks opens whole on l, and then mounts.
func TestLOnAnAncestorOpensItWhole(t *testing.T) {
	f, v := expandRig("ccc")
	mid := "here" + mountSep + "arias.top.forks.mid"
	v.Items(100)
	v.Expand(mid)
	deadline := time.Now().Add(5 * time.Second)
	for !slices.Contains(drawnIDs(v), mid+mountSep+"mantra") {
		if time.Now().After(deadline) {
			t.Fatalf("l on %s did not draw its form: %v (opened %v)", mid, drawnIDs(v), opened(f))
		}
		time.Sleep(10 * time.Millisecond)
	}
	if ids := strings.Join(drawnIDs(v), " "); !strings.Contains(ids, mid+".forks.me") {
		t.Errorf("opening %s whole lost its fork", mid)
	}
}

// A @recent row opens the same way: l on it shows the figaro's form.
func TestLOnARecentRowShowsItsForm(t *testing.T) {
	f := newFakeMounts()
	f.plain = map[string]bool{"aaa": true}
	f.forms["aaa"] = figaroForm("top")
	src := things("pushup.id", `"aaa"`, "pushup.title", `"top"`, "pushup.active_at", `1`)
	v := &formView{src: src, open: map[string]bool{}, opener: f.open}
	v.setView(&rpc.FormView{Rows: []string{"*"}, RowAria: "id", Mount: "id", Label: "title", Aside: "active_at"})
	v.Items(100)
	v.Expand("pushup")
	deadline := time.Now().Add(5 * time.Second)
	for !slices.Contains(drawnIDs(v), "pushup"+mountSep+"mantra") {
		if time.Now().After(deadline) {
			t.Fatalf("l on a recent row did not draw its form: %v", drawnIDs(v))
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A peer's form is the peer's to write: a row in it naming a third node's
// figaro must not send this client there. Expanding it mounts nothing and
// says why; a row naming the peer itself, spelled in full, still mounts.
func TestAPeersRowCannotMountAThirdNode(t *testing.T) {
	f, v := expandRig("none")
	f.forms["far/@things"] = things(
		"arias.stray.id", `"third/ddd"`, "arias.stray.mantra", `"points away"`,
		"arias.home.id", `"far/ddd"`, "arias.home.mantra", `"points home"`,
	)
	f.forms["third/ddd"], f.plain["third/ddd"] = figaroForm("elsewhere"), true
	v.Items(100)
	v.Activate("far")
	v.Items(100)
	f.waitOpened(t, 1)
	stray, home := "far"+mountSep+"arias.stray", "far"+mountSep+"arias.home"
	v.Items(100)
	v.Expand(stray)
	v.Expand(home)
	deadline := time.Now().Add(5 * time.Second)
	for !slices.Contains(opened(f), "far/ddd") {
		if time.Now().After(deadline) {
			t.Fatalf("the peer's own figaro did not mount: opened %v", opened(f))
		}
		v.Items(100)
		time.Sleep(10 * time.Millisecond)
	}
	if slices.Contains(opened(f), "third/ddd") {
		t.Fatalf("a peer's row sent the client to another node: opened %v", opened(f))
	}
	var said bool
	for _, r := range v.Items(100) {
		said = said || strings.Contains(r.str(), "refused: third/ddd is not on far")
	}
	if !said {
		t.Errorf("the refused row does not say why")
	}
}
