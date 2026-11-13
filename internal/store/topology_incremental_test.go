package store

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"reflect"
	"slices"
	"testing"

	"github.com/jack-work/figaro/api/form"
)

func snapshotDiff(inc, full *topologySnapshot) string {
	switch {
	case inc.version != full.version:
		return fmt.Sprintf("version %d, full %d", inc.version, full.version)
	case !reflect.DeepEqual(inc.infos, full.infos):
		return fmt.Sprintf("trunk infos differ:\n inc  %+v\n full %+v", inc.infos, full.infos)
	case !reflect.DeepEqual(inc.nodes, full.nodes):
		return fmt.Sprintf("nodes differ:\n inc  %+v\n full %+v", inc.nodes, full.nodes)
	case !reflect.DeepEqual(inc.conversations, full.conversations):
		return "conversations differ"
	case !reflect.DeepEqual(inc.forms, full.forms):
		return "forms differ"
	case !slices.Equal(inc.conversationIDs, full.conversationIDs):
		return "conversation ids differ"
	case !reflect.DeepEqual(inc.byID, full.byID):
		return "byID differs"
	}
	return ""
}

func fullSnapshot(s *XwalStore) *topologySnapshot {
	kept := s.topology.Load()
	s.topology.Store(nil)
	full := s.topologySnapshot()
	s.topology.Store(kept)
	return full
}

// A spawn patches the topology from the previous snapshot instead of
// reading every trunk's lineage again. Over a random run of creates, forks
// at the tail and inside, deletes, detaches, moves, promotes and sandbox
// sets and unsets, read at random points so that
// several spawns can land between two reads, the snapshot built that way
// must equal one built from nothing.
func TestIncrementalTopologyEqualsAFullRebuild(t *testing.T) {
	for _, seed := range []int64{1, 2, 3, 4, 5} {
		t.Run(fmt.Sprint(seed), func(t *testing.T) {
			runIncrementalTopology(t, seed, 160)
		})
	}
}

func runIncrementalTopology(t *testing.T, seed int64, steps int) {
	rng := rand.New(rand.NewSource(seed))
	dir := t.TempDir()
	b, err := NewXwalBackend(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	tree, err := OpenTopologyTree(b.Store(), dir)
	if err != nil {
		t.Fatal(err)
	}
	b.store.SetTree(tree)
	detached := 0
	b.SetDetachCarry(func(ids []string) error { detached += len(ids); return nil })
	seedTree(t, b, 2, 2, 1)
	outfit, _ := b.CreateOutfit("inc", patchSet(map[string]string{"system.model": "m"}))
	defaultForm, _, err := b.CreateForm("", form.Build(form.Snapshot{}, map[string]json.RawMessage{"system.provider": json.RawMessage(`"mock"`)}, nil))
	if err != nil {
		t.Fatal(err)
	}
	birth := func(i int) form.Patch {
		return form.Build(form.Snapshot{}, map[string]json.RawMessage{"n": json.RawMessage(fmt.Sprint(i))}, nil)
	}
	convs := func() []string { return b.store.ConversationIDs() }
	pick := func() string {
		ids := convs()
		if len(ids) == 0 {
			return ""
		}
		return ids[rng.Intn(len(ids))]
	}

	ops := map[string]int{}
	incremental := 0
	for i := 0; i < steps; i++ {
		before := trunkScanCount.Load()
		op := ""
		switch r := rng.Intn(100); {
		case r < 25:
			op = "new"
			_, _, err = b.ForkWith(defaultForm, 0, birth(i))
		case r < 35:
			op = "conversation"
			_, err = b.CreateConversation(outfit)
		case r < 55:
			op = "fork"
			if p := pick(); p != "" {
				turn(t, b, p, 1)
				_, _, err = b.ForkWith(p, 0, birth(i))
			}
		case r < 65:
			op = "fork-inside"
			if p := pick(); p != "" {
				turn(t, b, p, 2)
				_, _, err = b.ForkWith(p, 1, birth(i))
			}
		case r < 78:
			op = "delete"
			if p := pick(); p != "" {
				err = b.Remove(p, rng.Intn(2) == 0)
			}
		case r < 82:
			op = "detach"
			q := pick()
			var p, c string
			if p, _, err = b.ForkWith(defaultForm, 0, birth(i)); err == nil && q != "" {
				turn(t, b, p, 2)
				if c, _, err = b.ForkWith(p, 0, birth(i)); err == nil {
					if err = b.Move(c, q); err == nil {
						err = b.Remove(p, true)
					}
				}
			}
		case r < 85:
			op = "promote"
			if p := pick(); p != "" {
				_, err = b.Promote(p, 1)
			}
		case r < 88:
			op = "sandbox"
			if p := pick(); p != "" {
				if rng.Intn(2) == 0 {
					_, err = b.ApplyForm(p, form.Build(form.Snapshot{}, map[string]json.RawMessage{"system.sandbox": json.RawMessage(`"local"`)}, nil))
				} else {
					_, err = b.ApplyForm(p, form.Build(form.Snapshot{}, nil, []string{"system.sandbox"}))
				}
			}
		case r < 93:
			op = "move"
			ids := convs()
			if len(ids) >= 2 {
				a, u := ids[rng.Intn(len(ids))], ids[rng.Intn(len(ids))]
				if a != u {
					err = b.Move(a, u)
				}
			}
		default:
			op = "read"
		}
		if err != nil {
			t.Logf("step %d %s: %v (allowed: the run only needs the topology to agree)", i, op, err)
			err = nil
		}
		ops[op]++
		if rng.Intn(3) == 0 && op != "read" {
			continue
		}
		inc := b.store.topologySnapshot()
		if trunkScanCount.Load()-before == 1 {
			incremental++
		}
		if d := snapshotDiff(inc, fullSnapshot(b.store)); d != "" {
			t.Fatalf("seed %d step %d after %s: %s", seed, i, op, d)
		}
	}
	if incremental == 0 || detached == 0 {
		t.Fatalf("the run never read incrementally (%d) or never detached a survivor (%d): %v", incremental, detached, ops)
	}
	t.Logf("ops %v; %d reads incremental; %d survivors detached", ops, incremental, detached)
}

// Canary: the comparison is not vacuous. A snapshot missing the last
// spawned trunk, or carrying a stale fork base, must be reported.
func TestIncrementalTopologyCanary(t *testing.T) {
	b, err := NewXwalBackend(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	seedTree(t, b, 1, 2, 1)
	full := fullSnapshot(b.store)
	clone := func() *topologySnapshot {
		return &topologySnapshot{
			infos: slices.Clone(full.infos), version: full.version, rev: full.rev,
			nodes: full.nodes, conversations: full.conversations, forms: full.forms,
			conversationIDs: full.conversationIDs, byID: full.byID,
		}
	}
	if d := snapshotDiff(clone(), full); d != "" {
		t.Fatalf("an exact copy compared unequal: %s", d)
	}
	short := clone()
	short.infos = short.infos[:len(short.infos)-1]
	if snapshotDiff(short, full) == "" {
		t.Fatal("a snapshot missing a trunk compared equal")
	}
	stale := clone()
	stale.infos[len(stale.infos)-1].BranchedLT++
	if snapshotDiff(stale, full) == "" {
		t.Fatal("a snapshot with a stale fork base compared equal")
	}
	moved := clone()
	moved.version--
	if snapshotDiff(moved, full) == "" {
		t.Fatal("a snapshot at another version compared equal")
	}
}
