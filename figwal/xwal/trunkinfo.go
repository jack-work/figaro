package xwal

import (
	"fmt"
	"slices"
)

// ownerOf is the nearest ancestor whose own range covers atMainLT. It climbs
// the lineage, not the directory: every flat node is depth-1. The root owns
// [1..] and is returned as "".
func (t *Trunks) ownerOf(node string, atMainLT uint64) (string, error) {
	for hops := t.idx.Len(); node != ""; node = t.idx.ParentOf(node) {
		if hops--; hops < 0 {
			return "", fmt.Errorf("xwal: lineage cycle below %q", node)
		}
		fb, err := t.readForkBase(node)
		if err != nil {
			return "", err
		}
		if fb <= atMainLT {
			return node, nil
		}
	}
	return "", nil
}

// Owner: an LT owned by the root or a stump is ceremonial, so a fork there
// spawns a fresh child rather than re-splitting.
type Owner struct {
	Trunk  TrunkID // "" if the owner is the root or a stump
	Stump  string  // the stump name, if the owner is a stump
	IsRoot bool
}

// Owner resolves which node owns atMainLT along the given trunk's lineage.
func (t *Trunks) Owner(trunk TrunkID, atMainLT uint64) (Owner, error) {
	endRead, err := t.beginTrackedRead()
	if err != nil {
		return Owner{}, err
	}
	defer endRead()
	branch, err := t.headKey(trunk)
	if err != nil {
		return Owner{}, err
	}
	ob, err := t.ownerOf(branch, atMainLT)
	if err != nil {
		return Owner{}, err
	}
	n := t.node(ob)
	if n == nil {
		return Owner{}, fmt.Errorf("xwal: no owner node for main-LT %d on trunk %q", atMainLT, trunk)
	}
	return Owner{Trunk: n.Trunk, Stump: stumpName(ob, n), IsRoot: n.IsRoot}, nil
}

// anchorOf is where children of a trunk attach: its live head, else its
// single ceremonial node.
func (t *Trunks) anchorOf(trunk TrunkID) (string, bool) {
	if k, ok := t.idx.Head(trunk); ok {
		return k, true
	}
	for k, n := range t.idx.All() {
		if n.Trunk == trunk {
			return k, true
		}
	}
	return "", false
}

type TrunkInfo struct {
	ID         string
	Head       []string // head node branch
	Parent     string   // parent trunk id ("" if rooted at a stump or the root)
	Stump      string   // stump name, if the trunk is rooted directly at a stump
	Kind       string   // the founding node's kind, from its marker ("conversation", "form", …)
	BranchedLT uint64
	// BranchedTurn is BranchedLT in the caller's turn coordinate, recorded by
	// whoever knew it (SetBranchedTurn); xwal never computes one. Known is
	// false when nobody recorded it; Exact means the parent had a record at
	// the base when recorded. Filled by InfoLight only: a listing would pay a
	// second marker read per trunk.
	BranchedTurn      uint64
	BranchedTurnKnown bool
	BranchedTurnExact bool
	Tip               uint64
}

// List returns every trunk, in id order.
func (t *Trunks) List() []TrunkInfo {
	endRead, err := t.beginTrackedRead()
	if err != nil {
		return nil
	}
	defer endRead()
	ids := t.idx.LiveTrunks()
	out := make([]TrunkInfo, 0, len(ids))
	for _, id := range ids {
		unlockLineage := t.lockLineage(id)
		key := t.head(id)
		ti := TrunkInfo{ID: id, Head: []string{key}}
		if n := t.node(key); n != nil {
			ti.Kind = n.Kind
		}
		ti.Parent, ti.Stump, ti.BranchedLT = t.lineage(id)
		if x, err := t.openHotTopology(key); err == nil {
			ti.Tip = mainTail(x)
			x.Close()
		}
		unlockLineage()
		out = append(out, ti)
	}
	return out
}

// lineage finds a trunk's founding node (shallowest node carrying the
// trunk), returning its parent trunk id, the stump it is rooted at (if its
// parent is a stump), and the LT it branched at.
func (t *Trunks) lineage(trunk string) (parent, stump string, bl uint64) {
	head := t.head(trunk)
	n := t.node(head)
	if n == nil || n.IsRoot {
		return "", "", 0
	}
	// Flat: one node per trunk, so the head is the founding node and its
	// parent is From.
	p := t.node(n.From)
	if p == nil {
		return "", "", 0
	}
	return p.Trunk, stumpName(n.From, p), t.forkBaseOf(head)
}

// InfoLight is ListLight for one trunk plus the recorded base turn, without
// visiting every other trunk.
func (t *Trunks) InfoLight(id string) (TrunkInfo, bool) {
	endRead, err := t.beginTrackedRead()
	if err != nil {
		return TrunkInfo{}, false
	}
	defer endRead()
	key, ok := t.idx.Head(id)
	if !ok {
		return TrunkInfo{}, false
	}
	ti := TrunkInfo{ID: id, Head: []string{key}}
	if n := t.node(key); n != nil {
		ti.Kind = n.Kind
	}
	ti.Parent, ti.Stump, ti.BranchedLT = t.lineage(id)
	if ti.BranchedLT > 0 {
		ti.BranchedTurn, ti.BranchedTurnKnown, ti.BranchedTurnExact = t.forkTurnOf(key, ti.BranchedLT)
	}
	return ti, true
}

// ListLight is List without Tip, which needs the head's log opened; it opens
// no logs, so it is O(trunks) with no per-trunk disk scan.
func (t *Trunks) ListLight() []TrunkInfo {
	endRead, err := t.beginTrackedRead()
	if err != nil {
		return nil
	}
	defer endRead()
	ids := t.idx.LiveTrunks()
	out := make([]TrunkInfo, 0, len(ids))
	for _, id := range ids {
		out = append(out, t.lightInfo(id, t.head(id)))
	}
	return out
}

func (t *Trunks) lightInfo(id, key string) TrunkInfo {
	ti := TrunkInfo{ID: id, Head: []string{key}}
	if n := t.node(key); n != nil {
		ti.Kind = n.Kind
	}
	ti.Parent, ti.Stump, ti.BranchedLT = t.lineage(id)
	return ti
}

// ListLightSince is ListLight from a previous answer: prev was exact at
// version since, and when only new trunks were spawned after it, just
// those are read. Anything else is a full ListLight. The version returned
// is the one the answer is exact at; listed says the full walk ran.
func (t *Trunks) ListLightSince(prev []TrunkInfo, since uint64) (infos []TrunkInfo, at uint64, listed bool) {
	born, at, ok := t.idx.SpawnedSince(since)
	if !ok {
		v := t.idx.Version()
		return t.ListLight(), v, true
	}
	if len(born) == 0 {
		return prev, at, false
	}
	endRead, err := t.beginTrackedRead()
	if err != nil {
		v := t.idx.Version()
		return t.ListLight(), v, true
	}
	defer endRead()
	have := make(map[string]bool, len(prev))
	for _, ti := range prev {
		have[ti.ID] = true
	}
	out := append(make([]TrunkInfo, 0, len(prev)+len(born)), prev...)
	for _, id := range born {
		key, live := t.idx.Head(id)
		if have[id] || !live {
			continue
		}
		out = append(out, t.lightInfo(id, key))
	}
	slices.SortFunc(out, func(a, b TrunkInfo) int { return t.idx.compareTrunks(a.ID, b.ID) })
	return out, at, false
}

// Nodes returns every node (debug).
func (t *Trunks) Nodes() map[string]NodeInfo {
	endRead, err := t.beginTrackedRead()
	if err != nil {
		return nil
	}
	defer endRead()
	out := map[string]NodeInfo{}
	for key, n := range t.idx.All() {
		out[key] = *n
	}
	return out
}

// Kind reads the founding node's kind from the index; no log opened.
func (t *Trunks) Kind(trunk TrunkID) (string, bool) {
	endRead, err := t.beginTrackedRead()
	if err != nil {
		return "", false
	}
	defer endRead()
	key, ok := t.anchorOf(trunk)
	if !ok {
		return "", false
	}
	n := t.node(key)
	if n == nil {
		return "", false
	}
	return n.Kind, true
}
