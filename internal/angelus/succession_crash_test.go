package angelus

import (
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/hierarchy"
)

type processDied struct{}

func diedDuring(fn func()) (died bool) {
	defer func() {
		if r := recover(); r != nil {
			if _, ok := r.(processDied); !ok {
				panic(r)
			}
			died = true
		}
	}()
	fn()
	return false
}

type diesAtTheSeat struct{ hierarchy.Engine }

func (diesAtTheSeat) WriteDrawn(map[string]string, []string) error { panic(processDied{}) }

func restartDaemon(f seatFixture) {
	a := f.hs.h.angelus
	a.Backend = f.b
	a.Topology = hierarchy.New(f.b.HierarchyEngine(), a.DefaultTopologyGuard())
	f.hs.h.seated = sync.Map{}
	f.hs.RecoverSuccessions()
}

func liveSuccessorsBeside(f seatFixture, known map[string]bool) int {
	n := 0
	for _, node := range f.b.Nodes() {
		if !known[node.ID] && f.hs.h.angelus.isLiveAria(node.ID) {
			n++
		}
	}
	return n
}

func knownNodes(f seatFixture) map[string]bool {
	known := map[string]bool{}
	for _, n := range f.b.Nodes() {
		known[n.ID] = true
	}
	return known
}

func stampedForks(f seatFixture) []string {
	var out []string
	for _, v := range f.b.EveryNode() {
		if meta, _ := f.b.Meta(v.ID); meta != nil && meta.SuccessionOf != "" {
			out = append(out, v.ID)
		}
	}
	return out
}

func TestADaemonThatDiedMidSuccessionLeavesNoSuccessorBesideALiveSource(t *testing.T) {
	stages := map[string]func(f seatFixture, dying *bool){
		"between the fork and the seat": func(f seatFixture, dying *bool) {
			a := f.hs.h.angelus
			a.Topology = hierarchy.New(diesAtTheSeat{f.b.HierarchyEngine()}, a.DefaultTopologyGuard())
		},
		"between the seat and the retire": func(f seatFixture, dying *bool) {
			f.hs.h.angelus.Backend = faultyBackend{Backend: f.b, fail: func(id, patch string) error {
				if id == f.pred && strings.Contains(patch, `"retired"`) {
					panic(processDied{})
				}
				return nil
			}}
		},
	}
	for name, die := range stages {
		t.Run(name, func(t *testing.T) {
			f := newSeatFixture(t)
			setMaxChildren(t, f.b, f.pred, 1)
			setMaxChildren(t, f.b, f.top, 1)
			role := roleFor(t, f.hs, f.pred)
			known := knownNodes(f)
			ids := []string{f.pred, f.top, f.kid}
			before := boardsOf(t, f, ids...)
			for range 3 {
				die(f, nil)
				if !diedDuring(func() { callForkAsFigaro(t, f.hs, f.pred, succeedReq(f.pred, role)) }) {
					t.Fatal("the injected death did not happen")
				}
			}
			if len(stampedForks(f)) != 3 {
				t.Fatalf("the dead successions left %d stamped forks, want 3", len(stampedForks(f)))
			}

			restartDaemon(f)

			if n := len(stampedForks(f)); n != 0 {
				t.Fatalf("%d stamped forks survived the restart", n)
			}
			if n := liveSuccessorsBeside(f, known); n != 0 || !f.hs.h.angelus.isLiveAria(f.pred) {
				t.Fatalf("after the restart %d successor(s) live beside the source (source live=%v)", n, f.hs.h.angelus.isLiveAria(f.pred))
			}
			requireUntouched(t, f, before, ids...)

			if _, err := callForkAsFigaro(t, f.hs, f.pred, succeedReq(f.pred, role)); err != nil {
				t.Fatalf("the 4th succession was refused: %v", err)
			}
			if n := liveSuccessorsBeside(f, known); n != 1 || f.hs.h.angelus.isLiveAria(f.pred) {
				t.Fatalf("after the 4th succession %d successor(s) live and the source live=%v, want 1 and retired", n, f.hs.h.angelus.isLiveAria(f.pred))
			}
			if n := len(stampedForks(f)); n != 0 {
				t.Fatalf("a finished succession left %d stamped forks", n)
			}
		})
	}
}

func TestARestartKeepsTheSuccessorOfASourceThatWasRetired(t *testing.T) {
	f := newSeatFixture(t)
	role := roleFor(t, f.hs, f.pred)
	a := f.hs.h.angelus
	a.Backend = faultyBackend{Backend: f.b, fail: func(id, patch string) error {
		if strings.Contains(patch, form.SuccessionOfKey) || strings.Contains(patch, "succession_of") {
			return errors.New("injected: the stamp stays")
		}
		return nil
	}}
	out, err := callForkAsFigaro(t, f.hs, f.pred, succeedReq(f.pred, role))
	if err != nil {
		t.Fatal(err)
	}
	if got := stampedForks(f); len(got) != 1 || got[0] != out.Alternative {
		t.Fatalf("stamped forks %v, want the successor alone", got)
	}

	restartDaemon(f)

	if n := len(stampedForks(f)); n != 0 {
		t.Fatalf("the restart left %d stamps beside a retired source", n)
	}
	if !a.isLiveAria(out.Alternative) || a.isLiveAria(f.pred) {
		t.Fatal("the restart undid a succession whose source was retired")
	}
	if got := f.drawn(f.pred); got != out.Alternative {
		t.Fatalf("the source is drawn under %q, want the successor %s", got, out.Alternative)
	}
}

func TestAForkOfAStampedForkIsNotStamped(t *testing.T) {
	f := newSeatFixture(t)
	role := roleFor(t, f.hs, f.pred)
	a := f.hs.h.angelus
	a.Topology = hierarchy.New(diesAtTheSeat{f.b.HierarchyEngine()}, a.DefaultTopologyGuard())
	if !diedDuring(func() { callForkAsFigaro(t, f.hs, f.pred, succeedReq(f.pred, role)) }) {
		t.Fatal("the injected death did not happen")
	}
	stamped := stampedForks(f)
	if len(stamped) != 1 {
		t.Fatalf("stamped forks %v, want one", stamped)
	}
	if snap, _ := f.b.FormState(stamped[0]); !form.RuntimeHas(snap, form.SuccessionOfKey) {
		t.Fatal("the fork's form carries no stamp")
	}
	forked, err := callFork(t, f.hs, rpc.ForkRequest{FigaroID: stamped[0]})
	if err != nil {
		t.Fatal(err)
	}
	kin := forked.Alternative
	if snap, _ := f.b.FormState(kin); form.RuntimeHas(snap, form.SuccessionOfKey) {
		t.Fatal("a fork of a stamped fork inherited the stamp")
	}
	if meta, _ := f.b.Meta(kin); meta != nil && meta.SuccessionOf != "" {
		t.Fatal("a fork of a stamped fork is stamped in its sidecar")
	}
}
