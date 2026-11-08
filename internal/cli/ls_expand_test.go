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
