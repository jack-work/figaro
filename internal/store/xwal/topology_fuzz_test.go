package xwal

import (
	"errors"
	"fmt"
	"math/rand"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

type fuzzTrunk struct {
	id, node, parent string
	appended         []string
	removed          bool
}

type fuzzModel struct {
	mu     sync.Mutex
	trunks map[string]*fuzzTrunk
	order  []string
}

func (m *fuzzModel) add(id, node, parent string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.trunks[id] = &fuzzTrunk{id: id, node: node, parent: parent}
	m.order = append(m.order, id)
}

func (m *fuzzModel) pick(r *rand.Rand) *fuzzTrunk {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.order) == 0 {
		return nil
	}
	return m.trunks[m.order[r.Intn(len(m.order))]]
}

func (m *fuzzModel) descendsFromRemoved(id string) string {
	for tr := m.trunks[id]; tr != nil; tr = m.trunks[tr.parent] {
		if tr.removed {
			return tr.id
		}
		if tr.parent == "" {
			break
		}
	}
	return ""
}

// Many arias forking, writing, reading, holding heads, cutting and being
// removed at once. Nothing a live aria wrote is lost, nothing removed comes
// back, before and after a reopen.
func TestConcurrentTopologyFuzz(t *testing.T) {
	seed := time.Now().UnixNano()
	if s := os.Getenv("XWAL_FUZZ_SEED"); s != "" {
		fmt.Sscan(s, &seed)
	}
	t.Logf("seed %d", seed)
	dir := t.TempDir()
	s, err := OpenStore(dir, StoreOptions{Main: "main", SegmentSize: 1024, IdleUnload: -1})
	if err != nil {
		t.Fatal(err)
	}
	m := &fuzzModel{trunks: map[string]*fuzzTrunk{}}
	for i := 0; i < 6; i++ {
		id, err := s.SpawnUnderRoot()
		if err != nil {
			t.Fatal(err)
		}
		node, _ := s.HeadNode(id)
		m.add(id, node, "")
	}
	const workers, ops = 8, 250
	var wg sync.WaitGroup
	errs := make(chan error, workers)
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			r := rand.New(rand.NewSource(seed + int64(w)))
			for op := 0; op < ops; op++ {
				tr := m.pick(r)
				if tr == nil {
					continue
				}
				switch k := r.Intn(100); {
				case k < 8:
					id, err := s.SpawnUnderRoot()
					if err != nil {
						errs <- fmt.Errorf("spawn: %w", err)
						return
					}
					node, _ := s.HeadNode(id)
					m.add(id, node, "")
				case k < 18:
					child, err := s.Trunks.ForkTail(tr.id)
					if errors.Is(err, ErrUnknownTrunk) {
						continue
					}
					if err != nil {
						errs <- fmt.Errorf("fork %s: %w", tr.id, err)
						return
					}
					node, _ := s.HeadNode(child)
					m.add(child, node, tr.id)
				case k < 55:
					payload := fmt.Sprintf(`"%s:%d:%d"`, tr.id, w, op)
					if _, err := s.Append(tr.id, "main", 0, []byte(payload), nil); err != nil {
						if errors.Is(err, ErrUnknownTrunk) || strings.Contains(err.Error(), "unknown trunk") {
							continue
						}
						errs <- fmt.Errorf("append %s: %w", tr.id, err)
						return
					}
					m.mu.Lock()
					tr.appended = append(tr.appended, payload)
					m.mu.Unlock()
				case k < 75:
					m.mu.Lock()
					want := append([]string(nil), tr.appended...)
					m.mu.Unlock()
					rs, err := s.RecordsFrom(tr.id, "main", 0, 0)
					if err != nil {
						continue
					}
					if !holdsAll(rs, want) {
						errs <- fmt.Errorf("read %s: lost a record it wrote (%d written, seed %d)", tr.id, len(want), seed)
						return
					}
				case k < 85:
					x, err := s.Trunks.Head(tr.id)
					if err != nil {
						continue
					}
					time.Sleep(time.Duration(r.Intn(3)) * time.Millisecond)
					_ = x.Close()
				case k < 90:
					_ = s.TruncateFrom(tr.id, "notes", 1)
				default:
					if err := s.Remove(tr.id, true); err != nil {
						if errors.Is(err, ErrUnknownTrunk) || strings.Contains(err.Error(), "unknown trunk") {
							continue
						}
						errs <- fmt.Errorf("remove %s: %w", tr.id, err)
						return
					}
					m.mu.Lock()
					tr.removed = true
					m.mu.Unlock()
				}
			}
		}(w)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for len(s.Trunks.PendingRemovals()) > 0 && time.Now().Before(deadline) {
		s.Trunks.drainRemovals()
		time.Sleep(5 * time.Millisecond)
	}
	if p := s.Trunks.PendingRemovals(); len(p) > 0 {
		t.Fatalf("removals still pending with every head closed: %+v", p)
	}
	verifyFuzz(t, "live", s, m)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = OpenStore(dir, StoreOptions{Main: "main", SegmentSize: 1024, IdleUnload: -1})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	verifyFuzz(t, "reopened", s, m)
}

func holdsAll(rs []Record, want []string) bool {
	have := make(map[string]int, len(rs))
	for _, r := range rs {
		have[string(r.Payload)]++
	}
	for _, w := range want {
		if have[w] == 0 {
			return false
		}
		have[w]--
	}
	return true
}

func verifyFuzz(t *testing.T, phase string, s *Store, m *fuzzModel) {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	live := 0
	for _, id := range m.order {
		tr := m.trunks[id]
		_, isLive := s.HeadNode(id)
		if gone := m.descendsFromRemoved(id); gone != "" {
			if isLive {
				t.Fatalf("%s: %s is live though %s was removed", phase, id, gone)
			}
		}
		if !isLive {
			if dirs := nodeDirs(t, s.Trunks, tr.node); len(dirs) != 0 && m.descendsFromRemoved(id) != "" {
				t.Fatalf("%s: removed %s left %v", phase, id, dirs)
			}
			continue
		}
		live++
		rs, err := s.RecordsFrom(id, "main", 0, 0)
		if err != nil {
			t.Fatalf("%s: read %s: %v", phase, id, err)
		}
		if !holdsAll(rs, tr.appended) {
			t.Fatalf("%s: %s lost records it wrote", phase, id)
		}
		for _, r := range rs {
			p := string(r.Payload)
			if p == genesisMarker {
				continue
			}
			owner := strings.SplitN(strings.Trim(p, `"`), ":", 2)[0]
			ok := false
			for a := tr; a != nil; a = m.trunks[a.parent] {
				if a.id == owner {
					ok = true
					break
				}
				if a.parent == "" {
					break
				}
			}
			if !ok {
				t.Fatalf("%s: %s reads a record of %s, not its lineage", phase, id, owner)
			}
		}
	}
	ents, _ := os.ReadDir(s.Trunks.root)
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), removalPrefix) {
			t.Fatalf("%s: plan %s left", phase, e.Name())
		}
	}
	t.Logf("%s: %d trunks, %d live", phase, len(m.order), live)
}
