// flatten.go: the v3 -> v4 layout migration.
package xwal

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/jack-work/figaro/internal/store/disk"
	"github.com/jack-work/figaro/internal/store/segment"
)

// FlattenPlan is computed and validated before anything is touched: a
// refusal discovered halfway through a move would leave the store part flat,
// part nested.
type FlattenPlan struct {
	Root     string
	Main     string
	Channels []flattenChannelPlan

	// Nodes is the main channel's node count, Nested how many of them sit
	// below depth 1. Nested == 0 with Markers == 0 means the store is
	// already v4 and Apply is a no-op.
	Nodes   int
	Nested  int
	Moves   int
	Markers int
}

type flattenChannelPlan struct {
	Name  string
	Nodes []flattenNode
}

// flattenNode is one directory's fate. Rel is where it sits today
// (channel-relative), Flat where it will sit. Marker is non-nil only in the
// main channel, and only where a .node must be written.
type flattenNode struct {
	Rel    string
	Flat   string
	Marker *nodeMarker
}

// PlanFlatten modifies nothing and takes no lock; safe on a live store.
func PlanFlatten(root string) (*FlattenPlan, error) {
	chanList, main, codec, err := flattenManifest(root)
	if err != nil {
		return nil, err
	}
	plan := &FlattenPlan{Root: root, Main: main}

	mainDir := filepath.Join(root, main)
	// Leaf uniqueness in main is checked first: planMainMarkers indexes nodes
	// by leaf, and a duplicate leaf would make it refuse with "not one chain"
	// when the truth is a collision.
	if err := checkLeafCollisions(mainDir, main); err != nil {
		return nil, fmt.Errorf("xwal: cannot flatten %s:\n  %s", root, err)
	}
	mainMarkers, err := planMainMarkers(mainDir)
	if err != nil {
		return nil, err
	}

	// Main first: its tree is the lineage authority, and every other
	// channel is checked against it.
	ordered := append([]string{main}, without(chanList, main)...)
	parentOf := map[string]string{} // leaf -> its parent's leaf, per the main channel
	var collisions, divergent []string

	for _, ch := range ordered {
		dir := filepath.Join(root, ch)
		paths, err := nodePaths(dir)
		if err != nil {
			return nil, err
		}
		// Uniqueness is checked PER CHANNEL, at migration time. That the
		// trees are identical in shape is an observation about one store,
		// not a property of the format.
		claimed := map[string]string{}
		cp := flattenChannelPlan{Name: ch}
		for _, rel := range paths {
			leaf := filepath.Base(rel)
			if owner, taken := claimed[leaf]; taken {
				collisions = append(collisions, fmt.Sprintf("%s: %q and %q both flatten to %q", ch, owner, rel, leaf))
				continue
			}
			claimed[leaf] = rel
			if ch == main {
				parentOf[leaf] = mainParentLeaf(dir, rel)
			} else if p := parentLeaf(rel); p != "" {
				// A channel node still nested under something the main
				// channel does not call its parent. Lineage comes from main,
				// so flattening would re-home this channel's data: the base
				// in its .fork is an index in ITS parent's numbering.
				if want, known := parentOf[leaf]; known && want != p {
					divergent = append(divergent, fmt.Sprintf(
						"%s: %q is nested under %q, but the main channel's lineage says %q", ch, rel, p, want))
				}
			}
			if err := checkForkMarker(dir, rel, codec); err != nil {
				return nil, err
			}
			node := flattenNode{Rel: rel, Flat: leaf}
			if ch == main {
				if m, ok := mainMarkers[rel]; ok {
					node.Marker = &m
					plan.Markers++
				}
			}
			if rel != leaf {
				plan.Moves++
			}
			cp.Nodes = append(cp.Nodes, node)
		}
		// Deepest first, so a node's descendants have already left before it
		// moves. Renaming a parent otherwise carries its children with it and
		// makes every path recorded below it stale.
		sort.Slice(cp.Nodes, func(i, j int) bool {
			di, dj := pathDepth(cp.Nodes[i].Rel), pathDepth(cp.Nodes[j].Rel)
			if di != dj {
				return di > dj
			}
			return cp.Nodes[i].Rel < cp.Nodes[j].Rel
		})
		if ch == main {
			plan.Nodes = len(cp.Nodes)
			for _, n := range cp.Nodes {
				if pathDepth(n.Rel) > 1 {
					plan.Nested++
				}
			}
		}
		plan.Channels = append(plan.Channels, cp)
	}
	if len(collisions) > 0 || len(divergent) > 0 {
		return nil, fmt.Errorf("xwal: cannot flatten %s:\n  %s\n"+
			"name mangling is deliberately not implemented; a store that needs it "+
			"must be migrated by hand",
			root, strings.Join(append(collisions, divergent...), "\n  "))
	}
	return plan, nil
}

// checkLeafCollisions: name mangling is deliberately not implemented, so this
// is a refusal, and it names every clash at once.
func checkLeafCollisions(chanDir, ch string) error {
	paths, err := nodePaths(chanDir)
	if err != nil {
		return err
	}
	claimed := map[string]string{}
	var clashes []string
	for _, rel := range paths {
		leaf := filepath.Base(rel)
		if owner, taken := claimed[leaf]; taken {
			clashes = append(clashes, fmt.Sprintf("%s: %q and %q both flatten to %q", ch, owner, rel, leaf))
			continue
		}
		claimed[leaf] = rel
	}
	if len(clashes) > 0 {
		return fmt.Errorf("%s", strings.Join(clashes, "\n  "))
	}
	return nil
}

// planMainMarkers runs before any move because of the one-head rule: what a
// node's marker should say depends on the other nodes carrying the same trunk
// id, a property of the whole channel.
func planMainMarkers(mainDir string) (map[string]nodeMarker, error) {
	paths, err := nodePaths(mainDir)
	if err != nil {
		return nil, err
	}
	markers := map[string]nodeMarker{}
	carriers := map[string][]string{} // trunk id -> paths claiming it
	for _, rel := range paths {
		m, need, err := plannedMarker(mainDir, rel)
		if err != nil {
			return nil, err
		}
		if need {
			markers[rel] = m
			if m.trunk != "" {
				carriers[m.trunk] = append(carriers[m.trunk], rel)
			}
			continue
		}
		// Already migrated: its marker is authoritative, and a node already
		// demoted carries no trunk, so it does not compete for the head.
		if existing, ok := readNodeMarker(filepath.Join(mainDir, rel)); ok && existing.trunk != "" {
			carriers[existing.trunk] = append(carriers[existing.trunk], rel)
		}
	}
	var tangled []string
	byLeaf := lineageIndex(paths)
	depth := lineageDepths(mainDir, paths, byLeaf)
	for trunk, rels := range carriers {
		if len(rels) < 2 {
			continue
		}
		// Rank by lineage depth, not path depth: an interrupted run leaves a
		// chain mixed (the deep node already at depth 1, its ancestor still
		// nested), and by path depth the chain check compares the wrong pair.
		sort.Slice(rels, func(i, j int) bool { return depth[rels[i]] < depth[rels[j]] })
		if !isLineageChain(mainDir, rels, byLeaf) {
			// Nodes claim one trunk without lying on one line of descent, so
			// there is no "the newest" to pick; choosing wrong publishes an
			// aria's older half as its present. A gap in the line is an
			// interrupted migration, which isLineageChain follows.
			tangled = append(tangled, fmt.Sprintf(
				"trunk %q is claimed by %v, and no one of them descends from the others", trunk, rels))
			continue
		}
		for _, rel := range rels[:len(rels)-1] {
			m, ok := markers[rel]
			if !ok {
				// An already-migrated head that a deeper node outranks: rewriting
				// it is the only way to leave one head.
				m, _ = readNodeMarker(filepath.Join(mainDir, rel))
			}
			m.trunk = ""
			markers[rel] = m
		}
	}
	if len(tangled) > 0 {
		return nil, fmt.Errorf("xwal: cannot flatten %s:\n  %s", mainDir, strings.Join(tangled, "\n  "))
	}
	return markers, nil
}

// lineageIndex maps every main-channel node's LEAF name to where it
// currently sits. Both the depth count and the ancestry test walk it, so
// they agree by construction.
func lineageIndex(paths []string) map[string]string {
	byLeaf := make(map[string]string, len(paths))
	for _, rel := range paths {
		byLeaf[filepath.Base(rel)] = rel
	}
	return byLeaf
}

// lineageDepths counts each main-channel node's ancestors, by the same
// parent relation the chain check uses -- a node's nesting while it is still
// nested, its .node once it has moved. Path depth is not a substitute: a
// half-migrated chain holds both kinds at once.
func lineageDepths(mainDir string, paths []string, byLeaf map[string]string) map[string]int {
	depth := make(map[string]int, len(paths))
	for _, rel := range paths {
		cur, d := rel, 0
		// Bounded by the node count: a marker cycle is corruption, and a
		// migration must refuse to hang on one.
		for range paths {
			next, ok := byLeaf[mainParentLeaf(mainDir, cur)]
			if !ok {
				break
			}
			d++
			cur = next
		}
		depth[rel] = d
	}
	return depth
}

// isLineageChain: ancestry, not parenthood.
func isLineageChain(mainDir string, rels []string, byLeaf map[string]string) bool {
	for i := 1; i < len(rels); i++ {
		if !isAncestorOf(mainDir, byLeaf, rels[i-1], rels[i]) {
			return false
		}
	}
	return true
}

// isAncestorOf walks up from desc looking for anc. Bounded by the node
// count, for the same reason the depth walk is: a marker cycle is
// corruption and must not hang the migration.
func isAncestorOf(mainDir string, byLeaf map[string]string, anc, desc string) bool {
	want, cur := filepath.Base(anc), desc
	for i := 0; i < len(byLeaf); i++ {
		parent := mainParentLeaf(mainDir, cur)
		if parent == "" {
			return false
		}
		if parent == want {
			return true
		}
		next, ok := byLeaf[parent]
		if !ok {
			return false
		}
		cur = next
	}
	return false
}

// parentLeaf is the leaf name of a nested path's parent, empty at depth 1.
func parentLeaf(rel string) string {
	if parent := filepath.Dir(rel); parent != "." {
		return filepath.Base(parent)
	}
	return ""
}

// mainParentLeaf is a main-channel node's parent, from whichever of the two
// records it has: its nesting if it is still nested, its .node if it has
// already been moved by an interrupted run.
func mainParentLeaf(mainDir, rel string) string {
	if p := parentLeaf(rel); p != "" {
		return p
	}
	if m, ok := readNodeMarker(filepath.Join(mainDir, rel)); ok {
		return m.from
	}
	return ""
}

// readLegacyPair reads the pre-.node identity: .from holding "parent\nkind",
// .trunk the trunk id. It lives only in the migration: the read path does
// not know these files exist.
func readLegacyPair(dir string) (n nodeMarker, ok bool) {
	b, err := os.ReadFile(filepath.Join(dir, legacyFromName))
	if err != nil {
		return nodeMarker{}, false
	}
	// Split before trimming: an empty parent leaves a leading newline that
	// TrimSpace would eat, collapsing two fields into one.
	parts := strings.SplitN(string(b), "\n", 2)
	if len(parts) != 2 {
		return nodeMarker{}, false
	}
	n.from, n.kind = strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
	if tb, terr := os.ReadFile(filepath.Join(dir, legacyTrunkName)); terr == nil {
		n.trunk = strings.TrimSpace(string(tb))
	}
	return n, true
}

// NeedsFlatten reads one file. A directory with no manifest is not a store
// yet, and a store from a newer build needs a newer binary, not a migration.
func NeedsFlatten(root string) (bool, error) {
	m, err := readManifestFile(root)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	return m.Layout < layoutVersion, nil
}

// errNoForkBase: a missing .fork means "owns its log from 1" to this build,
// which the move preserves; with no segment at 1 the base would be derived
// at open from the node's parent, and after flattening that parent is the
// channel root rather than the node it was nested under.
type errNoForkBase struct{ ch, rel string }

func (e *errNoForkBase) Error() string {
	return fmt.Sprintf("xwal: node %q in channel %q has no .fork and no segment at index 1; "+
		"its base is defined by its nesting and flattening would change it", e.rel, e.ch)
}

func checkForkMarker(chanDir, rel string, codec segment.SegmentCodec) error {
	dir := filepath.Join(chanDir, rel)
	if _, err := readForkBaseFile(filepath.Join(dir, ".fork")); err == nil {
		return nil
	}
	first, ok, err := firstSegmentBase(dir, codec)
	if err != nil {
		return err
	}
	if ok && first == 1 {
		return nil // "owns its log from 1", which the move preserves
	}
	return &errNoForkBase{ch: filepath.Base(chanDir), rel: rel}
}

// plannedMarker derives a .node from where the node sits: from is its
// parent's flat name (empty at depth 1), trunk comes from the legacy .trunk,
// and a v3 node with a trunk id is a conversation, without one a stump. need
// is false when the node already has a .node.
func plannedMarker(chanDir, rel string) (m nodeMarker, need bool, err error) {
	dir := filepath.Join(chanDir, rel)
	if _, ok := readNodeMarker(dir); ok {
		return nodeMarker{}, false, nil
	}
	// A store flattened before the markers merged is already at depth 1 and
	// carries the .from/.kind pair; it is taken as written.
	if legacy, ok := readLegacyPair(dir); ok {
		return legacy, true, nil
	}
	m.from = parentLeaf(rel)
	if b, rerr := os.ReadFile(filepath.Join(dir, legacyTrunkName)); rerr == nil {
		m.trunk = strings.TrimSpace(string(b))
	}
	m.kind = "loadout"
	if m.trunk != "" {
		// Including a depth-1 node with a trunk: a conversation forked from the
		// null root, which only wants a marker.
		m.kind = "conversation"
	}
	return m, true, nil
}

// FlattenReport is counts, not durations.
type FlattenReport struct {
	Nodes   int
	Moved   int
	Markers int
	Retired int // legacy .trunk/.from files removed
}

// Flatten plans and applies in one call, under the store lock, so it refuses
// to run against a live daemon.
func Flatten(root string) (FlattenReport, error) {
	plan, err := PlanFlatten(root)
	if err != nil {
		return FlattenReport{}, err
	}
	lock, err := lockRoot(root)
	if err != nil {
		return FlattenReport{}, err
	}
	defer unlockRoot(lock)
	// The shape can have changed between planning and locking, so plan
	// again now that we are the only writer. This is the plan that runs.
	plan, err = PlanFlatten(root)
	if err != nil {
		return FlattenReport{}, err
	}
	return plan.apply()
}

func (p *FlattenPlan) apply() (FlattenReport, error) {
	rep := FlattenReport{Nodes: p.Nodes}
	for _, cp := range p.Channels {
		dir := filepath.Join(p.Root, cp.Name)
		// A rename is durable when both directories it touches are synced.
		// Every source parent must be synced too, or a crash can leave a node
		// visible at both paths and the resumed run refuses on collision.
		touched := map[string]bool{}
		syncTouched := func(d string) error {
			if !touched[d] {
				return nil
			}
			delete(touched, d)
			return disk.SyncDir(d)
		}
		for _, n := range cp.Nodes {
			src := filepath.Join(dir, n.Rel)
			// Deepest first, so everything that left this directory already
			// has. Sync it before it goes: after the rename its old path is gone.
			if err := syncTouched(src); err != nil {
				return rep, err
			}
			if n.Marker != nil {
				if err := writeNodeMarker(src, *n.Marker); err != nil {
					return rep, err
				}
				rep.Markers++
				// Only once .node is durable: one identity file per node. Best
				// effort: a full or read-only volume must not strand a
				// half-migrated store.
				for _, legacy := range []string{legacyTrunkName, legacyFromName} {
					if err := os.Remove(filepath.Join(src, legacy)); err == nil {
						rep.Retired++
					}
				}
			}
			if n.Rel == n.Flat {
				continue // already at depth 1 under its final name
			}
			if err := os.Rename(src, filepath.Join(dir, fsName(n.Flat))); err != nil {
				return rep, fmt.Errorf("xwal: flatten %s/%s: %w", cp.Name, n.Rel, err)
			}
			touched[filepath.Dir(src)] = true
			rep.Moved++
		}
		for d := range touched {
			if err := disk.SyncDir(d); err != nil {
				return rep, err
			}
		}
		if err := disk.SyncDir(dir); err != nil {
			return rep, err
		}
	}
	// The stamp goes last: it is the store's only claim to be v4, and
	// openTrunks refuses without it, so an interrupted migration must leave
	// it absent.
	m, err := readManifestFile(p.Root)
	if err != nil {
		return rep, err
	}
	if m.Layout != layoutVersion {
		// Every store this runs on predates the cursor stamp, whether or not it
		// was ever nested: the stamp and this layout shipped together.
		m.UnstampedRecords, m.Layout = true, layoutVersion
		if err := writeManifest(p.Root, m); err != nil {
			return rep, err
		}
	}
	return rep, nil
}

// NestedNodes counts node directories below depth 1 across every channel;
// zero means flat.
func NestedNodes(root string) (int, error) {
	chanList, _, _, err := flattenManifest(root)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, ch := range chanList {
		paths, err := nodePaths(filepath.Join(root, ch))
		if err != nil {
			return 0, err
		}
		for _, rel := range paths {
			if pathDepth(rel) > 1 {
				n++
			}
		}
	}
	return n, nil
}

// nodePaths is every node directory under a channel dir, as channel-relative
// paths, parents before children. A node directory is any directory whose
// name does not begin with a dot.
func nodePaths(chanDir string) ([]string, error) {
	if _, err := os.Stat(chanDir); os.IsNotExist(err) {
		return nil, nil
	}
	var out []string
	var walk func(rel string) error
	walk = func(rel string) error {
		ents, err := os.ReadDir(filepath.Join(chanDir, rel))
		if err != nil {
			return err
		}
		for _, e := range ents {
			if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
				continue
			}
			child := keyName(e.Name())
			if rel != "" {
				child = filepath.Join(rel, keyName(e.Name()))
			}
			out = append(out, child)
			if err := walk(child); err != nil {
				return err
			}
		}
		return nil
	}
	if err := walk(""); err != nil {
		return nil, err
	}
	return out, nil
}

func pathDepth(rel string) int { return strings.Count(rel, string(os.PathSeparator)) + 1 }

// flattenManifest reads the channel list, the main channel's name and the
// codec. Detection and migration both go through it, so neither can invent a
// channel the store does not have.
func flattenManifest(root string) (chans []string, main string, codec segment.SegmentCodec, err error) {
	m, err := readManifestFile(root)
	if err != nil {
		return nil, "", nil, err
	}
	if codec, err = codecByName(m.Codec); err != nil {
		return nil, "", nil, err
	}
	for _, c := range m.Channels {
		chans = append(chans, c.Name)
	}
	return chans, m.Main, codec, nil
}

func without(all []string, drop string) []string {
	out := make([]string, 0, len(all))
	for _, s := range all {
		if s != drop {
			out = append(out, s)
		}
	}
	return out
}
