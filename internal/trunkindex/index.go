// Package trunkindex backs xwal's TopologyIndex with a persistent tree.
//
// xwal derives its node/trunk index by walking every node directory and
// reading a .trunk marker. That is correct and it is O(forest) per mutation.
// This keeps the same facts in a pstate.Model instead: O(log n) apply via path
// copying, lock-free readers, and a background writer that never blocks a
// caller.
//
// The markers stay ground truth. This is a maintained cache of them, and
// RebuildFrom is how it is recovered, which is what makes a failed write
// survivable and why no write-ahead log is needed.
package trunkindex

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/jack-work/figwal/xwal"
	"github.com/jack-work/pstate"
)

// Two namespaces in one tree, so a single Apply keeps them consistent. Parent
// and child are written together and therefore cannot disagree.
const (
	nodePfx  = "node/"
	trunkPfx = "trunk/"
)

type nodeRec struct {
	Branch   []string `json:"branch"`
	Trunk    string   `json:"trunk,omitempty"`
	Parent   string   `json:"parent"`
	Children []string `json:"children,omitempty"`
	IsRoot   bool     `json:"root,omitempty"`
	IsStump  bool     `json:"stump,omitempty"`
}

// Index is an xwal.TopologyIndex over pstate.
//
// seqMu guards only the mint counters. The tree itself needs no lock: readers
// take a root pointer, writers CAS a new one.
type Index struct {
	m        *pstate.Model
	seqMu    sync.Mutex
	nodeSeq  int
	trunkSeq int
	mintID   func() string
}

// New returns an index persisting to path. A missing file is an empty index;
// xwal calls RebuildFrom at open, which fills it from the markers.
func New(path string, mintTrunkID func() string) (*Index, error) {
	var initial pstate.Tree
	if s, err := pstate.Open(path); err == nil {
		if snap, rerr := s.Read(); rerr == nil {
			initial = snap.Tree()
		}
	}
	writer := pstate.WriterFunc(func(s pstate.Snapshot) error { return write(path, s) })
	x := &Index{m: pstate.NewModel(initial, writer), mintID: mintTrunkID}
	x.recoverSeqs()
	return x, nil
}

func write(path string, s pstate.Snapshot) error {
	st, err := pstate.Open(path)
	if err != nil {
		return err
	}
	patch := pstate.Patch{}
	cur, err := st.Read()
	if err != nil {
		return err
	}
	cur.Range(func(k string, _ pstate.Value) bool {
		if _, ok := s.Get(k); !ok {
			patch = patch.Delete(k)
		}
		return true
	})
	s.Range(func(k string, v pstate.Value) bool { patch = patch.Set(k, v); return true })
	_, err = st.Apply(patch)
	return err
}

func (x *Index) Close() error { return x.m.Close() }

// Flush blocks until the current state is durable. Only tests and shutdown
// need it; the whole point is that callers do not wait on the disk.
func (x *Index) Flush() error { return x.m.Flush() }

func (x *Index) Version() uint64 { return x.m.Snapshot().Version() }

func (x *Index) Node(key string) (*xwal.NodeInfo, bool) {
	v, ok := x.m.Get(nodePfx + key)
	if !ok {
		return nil, false
	}
	return toInfo(v)
}

func (x *Index) Head(trunk string) (string, bool) {
	v, ok := x.m.Get(trunkPfx + trunk)
	if !ok {
		return "", false
	}
	var head string
	if json.Unmarshal(v.Raw(), &head) != nil {
		return "", false
	}
	return head, true
}

func (x *Index) All() map[string]*xwal.NodeInfo {
	out := map[string]*xwal.NodeInfo{}
	x.m.Snapshot().Range(func(k string, v pstate.Value) bool {
		if strings.HasPrefix(k, nodePfx) {
			if n, ok := toInfo(v); ok {
				out[strings.TrimPrefix(k, nodePfx)] = n
			}
		}
		return true
	})
	return out
}

func (x *Index) Walk(fn func(string, *xwal.NodeInfo) bool) {
	x.m.Snapshot().Range(func(k string, v pstate.Value) bool {
		if !strings.HasPrefix(k, nodePfx) {
			return true
		}
		n, ok := toInfo(v)
		return !ok || fn(strings.TrimPrefix(k, nodePfx), n)
	})
}

// LiveTrunks keeps xwal's ordering rule: numeric for sequential t<N> ids so
// t2 precedes t10, lexical for caller-minted opaque ids.
func (x *Index) LiveTrunks() []string {
	snap := x.m.Snapshot()
	var ids []string
	snap.Range(func(k string, _ pstate.Value) bool {
		if strings.HasPrefix(k, trunkPfx) {
			ids = append(ids, strings.TrimPrefix(k, trunkPfx))
		}
		return true
	})
	if x.mintID != nil {
		sort.Strings(ids)
		return ids
	}
	sort.Slice(ids, func(i, j int) bool { return numSuffix(ids[i]) < numSuffix(ids[j]) })
	return ids
}

// Spawn adds one leaf: the child node, the parent gaining it, and the child
// trunk's head. One Apply, so a reader never sees half of it.
func (x *Index) Spawn(parent, child, trunk string, isStump bool) error {
	snap := x.m.Snapshot()
	p := pstate.Patch{}
	if pn, ok := getNode(snap, parent); ok && !contains(pn.Children, child) {
		pn.Children = append(pn.Children, child)
		p = setNode(p, parent, pn)
		if pn.Trunk != "" && pn.Trunk != trunk {
			p = p.Delete(trunkPfx + pn.Trunk) // no longer a live head
		}
	}
	p = setNode(p, child, nodeRec{Branch: split(child), Trunk: trunk, Parent: parent, IsStump: isStump})
	if trunk != "" {
		p = p.Set(trunkPfx+trunk, val(child))
	}
	x.bumpSeqs(child, trunk)
	_, err := x.m.Apply(p)
	return err
}

func (x *Index) Reassign(trunkByNodeKey map[string]string) error {
	snap := x.m.Snapshot()
	p := pstate.Patch{}
	touched := map[string]bool{}
	for key, trunk := range trunkByNodeKey {
		n, ok := getNode(snap, key)
		if !ok {
			return fmt.Errorf("trunkindex: reassign unknown node %q", key)
		}
		touched[n.Trunk], touched[trunk] = true, true
		n.Trunk = trunk
		p = setNode(p, key, n)
	}
	next := snap.Tree().Apply(p)
	for trunk := range touched {
		if trunk == "" {
			continue
		}
		p = rehead(p, next, trunk)
	}
	_, err := x.m.Apply(p)
	return err
}

func (x *Index) Drop(nodeKeys, trunkIDs []string) error {
	snap := x.m.Snapshot()
	p := pstate.Patch{}
	for _, key := range nodeKeys {
		if n, ok := getNode(snap, key); ok {
			if pn, ok := getNode(snap, n.Parent); ok {
				pn.Children = remove(pn.Children, key)
				p = setNode(p, n.Parent, pn)
			}
		}
		p = p.Delete(nodePfx + key)
	}
	for _, id := range trunkIDs {
		p = p.Delete(trunkPfx + id)
	}
	next := snap.Tree().Apply(p)
	seen := map[string]bool{}
	rangeNodes(next, func(_ string, n nodeRec) bool {
		if n.Trunk != "" && !seen[n.Trunk] {
			seen[n.Trunk] = true
			p = rehead(p, next, n.Trunk)
		}
		return true
	})
	_, err := x.m.Apply(p)
	return err
}

// RebuildFrom re-derives everything from the markers. The only path that walks.
func (x *Index) RebuildFrom(mainDir string) error {
	fresh := pstate.Tree{}
	heads := map[string]string{}
	var walk func(dir string, branch []string, parentKey string, isRoot bool) error
	walk = func(dir string, branch []string, parentKey string, isRoot bool) error {
		key := strings.Join(branch, "/")
		trunkID := readTrunkID(dir)
		ents, err := os.ReadDir(dir)
		if err != nil {
			return err
		}
		var kids []string
		for _, e := range ents {
			if e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
				kids = append(kids, e.Name())
			}
		}
		n := nodeRec{
			Branch: append([]string(nil), branch...), Trunk: trunkID, Parent: parentKey,
			IsRoot: isRoot, IsStump: !isRoot && trunkID == "" && len(branch) == 1,
		}
		for _, k := range kids {
			n.Children = append(n.Children, joinKey(branch, k))
		}
		fresh = fresh.Set(nodePfx+key, val(n))
		x.bumpSeqs(key, trunkID)
		if len(n.Children) == 0 && trunkID != "" {
			if prev, dup := heads[trunkID]; dup {
				return fmt.Errorf("trunkindex: trunk %q has multiple live heads %q and %q", trunkID, prev, key)
			}
			heads[trunkID] = key
			fresh = fresh.Set(trunkPfx+trunkID, val(key))
		}
		for _, k := range kids {
			if err := walk(filepath.Join(dir, k), append(append([]string(nil), branch...), k), key, false); err != nil {
				return err
			}
		}
		return nil
	}
	x.resetSeqs()
	if err := walk(mainDir, nil, "", true); err != nil {
		return err
	}
	// Replace wholesale: a rebuild is a repair, so anything not on disk is gone.
	p := pstate.Patch{}
	x.m.Snapshot().Range(func(k string, _ pstate.Value) bool { p = p.Delete(k); return true })
	fresh.Range(func(k string, v pstate.Value) bool { p = p.Set(k, v); return true })
	_, err := x.m.Apply(p)
	return err
}

func (x *Index) MintNode() string {
	x.seqMu.Lock()
	defer x.seqMu.Unlock()
	id := "n" + strconv.Itoa(x.nodeSeq)
	x.nodeSeq++
	return id
}

// MintTrunk and MintNode do not persist their counters and cannot fail.
//
// The alternative is a durable sequence, so a crash between minting an id and
// creating its directory cannot leak it. We are not doing that. The counters
// are recovered at open by RebuildFrom, which reads the n<N> and t<N> suffixes
// off directory names. A leaked id costs one skipped integer per crash and is
// corrected on the next open.
//
// The trade is deliberate: a durable counter puts a synchronous write on the
// create path, which is the path this exists to make cheap, to close a gap in
// a sequence nobody reads for meaning. Ids are opaque, gaps are not a defect.
func (x *Index) MintTrunk() string {
	if x.mintID != nil {
		return x.mintID()
	}
	x.seqMu.Lock()
	defer x.seqMu.Unlock()
	id := "t" + strconv.Itoa(x.trunkSeq)
	x.trunkSeq++
	return id
}

func (x *Index) resetSeqs() {
	x.seqMu.Lock()
	x.nodeSeq, x.trunkSeq = 0, 0
	x.seqMu.Unlock()
}

func (x *Index) recoverSeqs() {
	x.Walk(func(key string, n *xwal.NodeInfo) bool {
		x.bumpSeqs(key, n.Trunk)
		return true
	})
}

func (x *Index) bumpSeqs(key, trunkID string) {
	x.seqMu.Lock()
	defer x.seqMu.Unlock()
	if key != "" {
		seg := key
		if i := strings.LastIndex(key, "/"); i >= 0 {
			seg = key[i+1:]
		}
		if n := suffixNum(seg, 'n'); n+1 > x.nodeSeq {
			x.nodeSeq = n + 1
		}
	}
	if n := suffixNum(trunkID, 't'); n+1 > x.trunkSeq {
		x.trunkSeq = n + 1
	}
}

// --- helpers ---

func val(v any) pstate.Value { pv, _ := pstate.EncodeValue(v); return pv }
func split(key string) []string {
	if key == "" {
		return nil
	}
	return strings.Split(key, "/")
}

func setNode(p pstate.Patch, key string, n nodeRec) pstate.Patch { return p.Set(nodePfx+key, val(n)) }

func getNode(s pstate.Snapshot, key string) (nodeRec, bool) {
	v, ok := s.Get(nodePfx + key)
	if !ok {
		return nodeRec{}, false
	}
	var n nodeRec
	return n, json.Unmarshal(v.Raw(), &n) == nil
}

func rangeNodes(t pstate.Tree, fn func(string, nodeRec) bool) {
	t.Range(func(k string, v pstate.Value) bool {
		if !strings.HasPrefix(k, nodePfx) {
			return true
		}
		var n nodeRec
		if json.Unmarshal(v.Raw(), &n) != nil {
			return true
		}
		return fn(strings.TrimPrefix(k, nodePfx), n)
	})
}

// rehead points trunk at its one live leaf, or drops it when it has none.
func rehead(p pstate.Patch, next pstate.Tree, trunk string) pstate.Patch {
	found := ""
	rangeNodes(next, func(key string, n nodeRec) bool {
		if n.Trunk == trunk && len(n.Children) == 0 {
			found = key
			return false
		}
		return true
	})
	if found == "" {
		return p.Delete(trunkPfx + trunk)
	}
	return p.Set(trunkPfx+trunk, val(found))
}

func toInfo(v pstate.Value) (*xwal.NodeInfo, bool) {
	var n nodeRec
	if json.Unmarshal(v.Raw(), &n) != nil {
		return nil, false
	}
	return &xwal.NodeInfo{
		Branch: n.Branch, Trunk: n.Trunk, Parent: n.Parent,
		Children: n.Children, IsRoot: n.IsRoot, IsStump: n.IsStump,
	}, true
}

func readTrunkID(dir string) string {
	b, err := os.ReadFile(filepath.Join(dir, ".trunk"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func joinKey(branch []string, child string) string {
	if len(branch) == 0 {
		return child
	}
	return strings.Join(branch, "/") + "/" + child
}

func suffixNum(s string, p byte) int {
	if len(s) < 2 || s[0] != p {
		return -1
	}
	n, err := strconv.Atoi(s[1:])
	if err != nil {
		return -1
	}
	return n
}

func numSuffix(id string) int {
	if n := suffixNum(id, 't'); n >= 0 {
		return n
	}
	return 1 << 30
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

func remove(s []string, v string) []string {
	out := make([]string, 0, len(s))
	for _, x := range s {
		if x != v {
			out = append(out, x)
		}
	}
	return out
}
