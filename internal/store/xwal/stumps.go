package xwal

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// CreateStump mints a markerless, named depth-1 child of the root: the
// cauterization boundary. It holds its own birth content (StumpHead) and
// hosts top-level trunks; its name and depth-1 position are its identity. A
// duplicate name is an error.
func (t *Trunks) CreateStump(name string) error {
	endMutation, err := t.beginFlatCreate()
	if err != nil {
		return err
	}
	defer endMutation()
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, `/\`) || strings.HasPrefix(name, ".") {
		return fmt.Errorf("xwal: invalid stump name %q", name)
	}
	if n := t.node(name); n != nil {
		return fmt.Errorf("xwal: stump %q already exists", name)
	}
	if err := t.forkFlatNamed("", name, "loadout"); err != nil {
		return fmt.Errorf("xwal: create-stump %q: %w", name, err)
	}
	return nil
}

// RemoveStump deletes a childless stump.
func (t *Trunks) RemoveStump(name string) error {
	endMutation, err := t.beginTopologyMutation()
	if err != nil {
		return err
	}
	defer endMutation()
	if err := t.ensureNoOpenHeads(); err != nil {
		return err
	}
	t.retireRootHotPreservingValidation()

	n := t.node(name)
	if n == nil || n.Kind != "loadout" {
		return fmt.Errorf("%w %q", ErrUnknownStump, name)
	}
	if kids := t.idx.ChildrenOf(name); len(kids) > 0 {
		return fmt.Errorf("%w: %q has %d", ErrStumpHasChildren, name, len(kids))
	}

	names, err := channelNames(t.root)
	if err != nil {
		return err
	}
	for _, ch := range names {
		if err := os.RemoveAll(filepath.Join(t.root, ch, fsName(name))); err != nil {
			return err
		}
	}
	return t.rebuild()
}

// StumpHead opens a stump's branch for appending its birth content. Caller closes it.
func (t *Trunks) StumpHead(name string) (*XWAL, error) {
	releaseLineage := t.holdLineageHead("stump:" + name)
	if err := beginRootBorrow(t.registryRoot, t); err != nil {
		releaseLineage()
		return nil, err
	}
	if err := t.ensureCurrentTopology(); err != nil {
		endRootBorrow(t.registryRoot, t)
		releaseLineage()
		return nil, err
	}
	t.mu.RLock()
	n := t.node(name)
	if n == nil || n.Kind != "loadout" {
		t.mu.RUnlock()
		endRootBorrow(t.registryRoot, t)
		releaseLineage()
		return nil, fmt.Errorf("xwal: no stump %q", name)
	}
	x, release, err := t.borrowHotUntracked([]string{name})
	t.mu.RUnlock()
	if err != nil {
		endRootBorrow(t.registryRoot, t)
		releaseLineage()
		return nil, err
	}
	view := x.sharedView(release, func() {
		endRootBorrow(t.registryRoot, t)
	}, t.retireRootHotPreservingValidation)
	view.releaseLineage = releaseLineage
	view.borrowRoot = t.registryRoot
	view.borrowOwner = t
	return view, nil
}

func (t *Trunks) SpawnUnderStump(name string) (TrunkID, error) {
	return t.SpawnUnderStumpKind(name, "conversation")
}

// SpawnUnderStumpKind: the kind is recorded in the child's marker.
func (t *Trunks) SpawnUnderStumpKind(name, kind string) (TrunkID, error) {
	endMutation, err := t.beginFlatCreate()
	if err != nil {
		return "", err
	}
	defer endMutation()
	if n := t.node(name); n == nil || n.Kind != "loadout" {
		return "", fmt.Errorf("xwal: no stump %q", name)
	}
	return t.forkFlat(name, kind, true)
}

type StumpInfo struct {
	Name     string
	Children []TrunkID // trunk ids of its immediate trunk children
}

// Stumps returns every stump, sorted by name.
func (t *Trunks) Stumps() []StumpInfo {
	endRead, err := t.beginTrackedRead()
	if err != nil {
		return nil
	}
	defer endRead()
	var out []StumpInfo
	for key, n := range t.idx.All() {
		if n.Kind != "loadout" {
			continue
		}
		// Range the key: stumpName() returns "" for an empty Branch, which
		// would then ask for the root's children.
		si := StumpInfo{Name: key}
		for _, ck := range t.idx.ChildrenOf(key) {
			if c := t.node(ck); c != nil && c.Trunk != "" {
				si.Children = append(si.Children, c.Trunk)
			}
		}
		sort.Strings(si.Children)
		out = append(out, si)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
