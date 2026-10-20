package xwal

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

func (t *Trunks) readForkBase(key string) (uint64, error) {
	b, err := os.ReadFile(filepath.Join(t.irDir(key), ".fork"))
	if err != nil {
		return 0, err
	}
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), "base="); ok {
			return strconv.ParseUint(strings.TrimSpace(v), 10, 64)
		}
	}
	return 0, fmt.Errorf("xwal: malformed fork marker for %q", key)
}

// ForkTail mints a sibling sharing the trunk's whole present.
func (t *Trunks) ForkTail(trunk string) (string, error) {
	unlockLineage := t.lockLineage(trunk)
	defer unlockLineage()
	endMutation, err := t.beginFlatCreate()
	if err != nil {
		return "", err
	}
	defer endMutation()
	return t.forkTailLocked(trunk)
}

func (t *Trunks) forkTailLocked(trunk string) (string, error) {
	headKey, ok := t.idx.Head(trunk)
	if !ok {
		return "", fmt.Errorf("%w %q", ErrUnknownTrunk, trunk)
	}
	return t.forkFlat(headKey, "conversation", true)
}

// forkFlat mints a depth-1 sibling sharing the parent prefix. Nothing the
// parent owns is written: no freeze, no continuation, no rehome.
func (t *Trunks) forkFlat(parentKey, kind string, mintTrunk bool) (string, error) {
	child := t.mintNode()
	var trunk string
	if mintTrunk {
		trunk = t.mintTrunk(kind)
	}
	bases, err := t.channelBases(parentKey, 0)
	if err != nil {
		return "", err
	}
	for name, base := range bases {
		dir := filepath.Join(t.root, name, fsName(child))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return "", err
		}
		if err := writeSyncedFile(filepath.Join(dir, ".fork"), fmt.Appendf(nil, "base=%d\n", base)); err != nil {
			return "", err
		}
	}
	// One marker, written last: it is the commit point. Writing lineage and
	// trunk id as a pair left a node with one and not the other after a crash.
	if err := writeNodeMarker(t.irDir(child), nodeMarker{from: parentKey, kind: kind, trunk: trunk}); err != nil {
		return "", err
	}
	t.idx.SpawnFlat(parentKey, child, trunk, kind)
	if !mintTrunk {
		return child, nil
	}
	return trunk, nil
}

// forkFlatNamed is forkFlat with a caller-chosen node name (stumps).
func (t *Trunks) forkFlatNamed(parentKey, name, kind string) error {
	bases, err := t.channelBases(parentKey, 0)
	if err != nil {
		return err
	}
	for chName, base := range bases {
		dir := filepath.Join(t.root, chName, fsName(name))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
		if err := writeSyncedFile(filepath.Join(dir, ".fork"), fmt.Appendf(nil, "base=%d\n", base)); err != nil {
			return err
		}
	}
	if err := writeNodeMarker(t.irDir(name), nodeMarker{from: parentKey, kind: kind}); err != nil {
		return err
	}
	t.idx.SpawnFlat(parentKey, name, "", kind)
	return nil
}

// channelBases is the fork base for every channel of node, sharing main
// [1..at]. ONE rule for tail and interior forks alike: main starts at at+1,
// and a related channel inherits only what is keyed at or below at, never
// past what the parent itself exposes, never below where its segments start.
func (t *Trunks) channelBases(node string, at uint64) (map[string]uint64, error) {
	x, err := t.openHotTopology(node)
	if err != nil {
		return nil, err
	}
	// A tail fork means "everything as it stands now", which for an unkeyed
	// channel includes records written since the last turn. An interior fork
	// means "as of turn N", which is what N's cursor stamp says.
	tailFork := at == 0
	if tailFork {
		at = mainTail(x)
	}
	out := map[string]uint64{}
	for _, c := range x.Channels() {
		if c.Absent {
			continue
		}
		var base uint64
		switch {
		case c.Name == t.main:
			base = at + 1
		case x.channel(c.Name).unkeyed && tailFork:
			base = c.Last + 1
		case x.channel(c.Name).unkeyed:
			// An unkeyed channel carries no main LT; the main record at the
			// fork point recorded where it stood, and that stamp is the boundary.
			cur, cerr := x.CursorAt(at, c.Name)
			if cerr != nil {
				x.Close()
				return nil, cerr
			}
			base = cur + 1
			// Same ceiling as the keyed branch: a cursor is data on disk and can
			// name a position the parent does not have, and a base above the
			// parent's tail leaves the child numbering over a hole in its prefix.
			if base > c.Last+1 {
				base = c.Last + 1
			}
		default:
			lt, ok, lerr := x.channel(c.Name).lookupAtOrBelow(at)
			if lerr != nil {
				x.Close()
				return nil, lerr // a failed lookup is not "found nothing"
			}
			if !ok {
				lt = 0
			}
			base = lt + 1
			if base > c.Last+1 {
				base = c.Last + 1
			}
		}
		if base < c.First {
			base = c.First // channel empty here; inherit from the ancestor
		}
		out[c.Name] = base
	}
	x.Close()
	return out, nil
}

// ForkAt shares [1..atMainLT] and creates an empty alternative trunk
// diverging at atMainLT+1; the original keeps its id and its suffix. At or
// past the tail it degenerates to a tail fork.
func (t *Trunks) ForkAt(trunk string, atMainLT uint64) (string, error) {
	unlockLineage := t.lockLineage(trunk)
	defer unlockLineage()
	endMutation, err := t.beginFlatCreate()
	if err != nil {
		return "", err
	}
	defer endMutation()
	branch, err := t.headKey(trunk)
	if err != nil {
		return "", err
	}
	x, err := t.openHotTopology(branch)
	if err != nil {
		return "", err
	}
	tail := mainTail(x)
	ownFirst := ownFirstIdx(x)
	x.Close()

	if atMainLT == 0 || atMainLT >= tail {
		return t.forkTailLocked(trunk)
	}
	if atMainLT < ownFirst {
		// Flat: fork the ancestor that owns atMainLT, in place.
		t.retireRootHotPreservingValidation()
		owner, oerr := t.ownerOf(branch, atMainLT)
		if oerr != nil {
			return "", oerr
		}
		return t.forkFlatAt(owner, atMainLT)
	}

	// Interior fork: a sibling sharing [1..atMainLT]. The parent keeps its
	// suffix; nothing it owns is rewritten.
	return t.forkFlatAt(branch, atMainLT)
}

// forkFlatAt is forkFlat sharing a prefix that ends at an interior main LT.
// Related channels share up to their last entry keyed at or below it.
func (t *Trunks) forkFlatAt(parentKey string, atMainLT uint64) (string, error) {
	child := t.mintNode()
	// Interior forks re-split conversations; nothing else has a tail to
	// split at an interior LT.
	trunk := t.mintTrunk("conversation")
	bases, err := t.channelBases(parentKey, atMainLT)
	if err != nil {
		return "", err
	}
	for name, base := range bases {
		dir := filepath.Join(t.root, name, fsName(child))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return "", err
		}
		if err := writeSyncedFile(filepath.Join(dir, ".fork"), fmt.Appendf(nil, "base=%d\n", base)); err != nil {
			return "", err
		}
	}
	if err := writeNodeMarker(t.irDir(child), nodeMarker{from: parentKey, kind: "conversation", trunk: trunk}); err != nil {
		return "", err
	}
	t.idx.SpawnFlat(parentKey, child, trunk, "conversation")
	return trunk, nil
}

func (t *Trunks) SpawnChild(parent TrunkID) (TrunkID, error) {
	return t.SpawnChildKind(parent, "conversation")
}

// SpawnChildKind: the parent is not written, not frozen, and stays
// appendable; a fork snapshots the parent's channels at their bases and later
// parent appends belong to the parent alone.
func (t *Trunks) SpawnChildKind(parent TrunkID, kind string) (TrunkID, error) {
	endMutation, err := t.beginFlatCreate()
	if err != nil {
		return "", err
	}
	defer endMutation()
	nodeKey, ok := t.anchorOf(parent)
	if !ok {
		return "", fmt.Errorf("%w %q", ErrUnknownTrunk, parent)
	}
	return t.forkFlat(nodeKey, kind, true)
}

// SpawnUnderRoot mints a top-level trunk with no stump.
func (t *Trunks) SpawnUnderRoot() (TrunkID, error) {
	return t.SpawnUnderRootKind("conversation")
}

// SpawnUnderRootKind: the kind lands in the node marker, is immutable from
// then on (a fork never converts), and is what listings discriminate by.
func (t *Trunks) SpawnUnderRootKind(kind string) (TrunkID, error) {
	endMutation, err := t.beginFlatCreate()
	if err != nil {
		return "", err
	}
	defer endMutation()
	return t.forkFlat("", kind, true)
}

// forkTurnName is a derived sidecar beside .fork: self-validating (it names
// the base it is true of) and self-healing (absent, torn or stale, it is not
// believed). It is not part of .fork because .fork is load-bearing for
// opening the store.
const forkTurnName = ".forkturn"

// forkTurnOf reads a node's recorded base turn, if one was recorded for the
// base the node has now.
func (t *Trunks) forkTurnOf(key string, base uint64) (turn uint64, known, exact bool) {
	b, err := os.ReadFile(filepath.Join(t.irDir(key), forkTurnName))
	if err != nil {
		return 0, false, false
	}
	var gotBase, gotTurn bool
	var recBase uint64
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		v = strings.TrimSpace(v)
		switch k {
		case "base":
			n, perr := strconv.ParseUint(v, 10, 64)
			recBase, gotBase = n, perr == nil
		case "turn":
			n, perr := strconv.ParseUint(v, 10, 64)
			turn, gotTurn = n, perr == nil
		case "exact":
			exact = v == "true"
		}
	}
	if !gotBase || !gotTurn || recBase != base {
		return 0, false, false
	}
	return turn, true, exact
}

// SetBranchedTurn records the turn a trunk's fork base falls in, in the
// parent's turn space. base must be the base on disk now: a topology change
// that moved it must not inherit the number. The write is atomic and
// deliberately not synced: a lost sidecar is recomputed.
func (t *Trunks) SetBranchedTurn(trunk string, base, turn uint64, exact bool) error {
	endRead, err := t.beginTrackedRead()
	if err != nil {
		return err
	}
	defer endRead()
	key, ok := t.idx.Head(trunk)
	if !ok {
		return fmt.Errorf("%w %q", ErrUnknownTrunk, trunk)
	}
	if have := t.forkBaseOf(key); have != base {
		return fmt.Errorf("xwal: %s forks at LT %d, not %d: refusing to record its turn", trunk, have, base)
	}
	dir := t.irDir(key)
	f, err := os.CreateTemp(dir, "."+forkTurnName+"-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	if _, err := fmt.Fprintf(f, "base=%d\nturn=%d\nexact=%t\n", base, turn, exact); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, filepath.Join(dir, forkTurnName)); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// forkBaseOf reads the .fork marker directly; no log open.
func (t *Trunks) forkBaseOf(key string) uint64 {
	b, err := os.ReadFile(filepath.Join(t.irDir(key), ".fork"))
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), "base="); ok {
			n, _ := strconv.ParseUint(strings.TrimSpace(v), 10, 64)
			return n
		}
	}
	return 0
}
