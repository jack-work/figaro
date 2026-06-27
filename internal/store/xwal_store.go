package store

// XwalStore is the figaro aria tree on figwal's xwal: one fork tree
// rooted at the data dir (the "arias" null root). Every aria is a node:
//
//	null (arias) ──fork──> loadout node ──fork──> conversation ──fork──> branch…
//
//   - null: the root dir itself, seeded with a genesis tic + root default
//     chalkboard, then a frozen branch point.
//   - loadout node: fork(null) + the loadout's chalkboard patch, stamped
//     with immutable system.loadout_name / system.loadout_version. One per
//     (name, content-version).
//   - conversation: fork(loadout) — inherits defaults via the chalkboard
//     watermark. Branching a conversation is the same fork op.
//
// A node is addressed by its branch path (the chain of fork names from
// the root); a persisted index maps aria id -> branch path and
// (loadout, version) -> node id.

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/jack-work/figaro/internal/chalkboard"
	"github.com/jack-work/figaro/internal/message"
	"github.com/jack-work/figwal/segment"
	"github.com/jack-work/figwal/xwal"
)

const (
	NullAriaID       = "arias"
	chanIR           = "ir"
	chanChalkboard   = "chalkboard"
	reducerJSONMerge = "jsonmerge"
	indexFile        = "index.json"
	// oldFutureName is the continuation name passed to xwal.Fork. For the
	// tail forks used to spawn nodes there is no suffix, so no such subdir
	// is created; it only materializes when branching mid-conversation.
	contName = "_cont"

	keyLoadoutName = "system.loadout_name"
	keyLoadoutVer  = "system.loadout_version"
)

// chalkboardReduce folds a message.Patch (JSON) onto a chalkboard
// snapshot (JSON state) — the reducer for the chalkboard channel.
func chalkboardReduce(state, patch []byte) ([]byte, error) {
	snap := chalkboard.Snapshot{}
	if len(state) > 0 {
		if err := json.Unmarshal(state, &snap); err != nil {
			return nil, err
		}
	}
	var p message.Patch
	if err := json.Unmarshal(patch, &p); err != nil {
		return nil, err
	}
	return json.Marshal(snap.Apply(p))
}

func storeConfig() xwal.Config {
	return xwal.Config{
		Main:  chanIR,
		Codec: "jsonl",
		Registry: map[string]xwal.Reducer{
			reducerJSONMerge: {Reduce: chalkboardReduce, Initial: []byte("{}")},
		},
		Channels: []xwal.ChannelSpec{
			{Name: chanIR, Kind: xwal.ChannelLog},
			{Name: chanChalkboard, Kind: xwal.ChannelReducible, Reducer: reducerJSONMerge},
		},
	}
}

type nodeKind string

const (
	kindNull         nodeKind = "null"
	kindLoadout      nodeKind = "loadout"
	kindConversation nodeKind = "conversation"
)

type nodeRec struct {
	ID        string   `json:"id"`
	Branch    []string `json:"branch"` // fork path from the root
	Parent    string   `json:"parent,omitempty"`
	Kind      nodeKind `json:"kind"`
	Loadout   string   `json:"loadout,omitempty"`
	Version   string   `json:"version,omitempty"`
	Children  []string `json:"children,omitempty"`
	Frozen    bool     `json:"frozen,omitempty"`
	CreatedMS int64    `json:"created_ms"`
	LastMS    int64    `json:"last_ms,omitempty"`

	// Trunk + Vector model the conversation forest for the UI.
	// Trunk is the thread identity that flows DOWN the continuation line:
	// a root conversation founds its own trunk (Trunk == its own id), a
	// fork's continuation inherits the parent's Trunk, and a fork's
	// alternative founds a new trunk. Vector is the child-index path from
	// the root conversation (continuation appends 0, alternative 1), so it
	// reads as 0, 0.0, 0.0.0 down a trunk and 0.1, 0.1.0 for an alternate
	// limb. Both are empty for the null/loadout infrastructure nodes.
	Trunk  string `json:"trunk,omitempty"`
	Vector []int  `json:"vector,omitempty"`
}

type nodeIndex struct {
	Nodes    map[string]*nodeRec `json:"nodes"`
	Loadouts map[string]string   `json:"loadouts"` // "name@version" -> node id
}

// XwalStore owns the aria tree.
type XwalStore struct {
	root string
	cfg  xwal.Config
	mu   sync.Mutex
	idx  *nodeIndex
	now  func() int64
}

// OpenXwalStore opens (creating if absent) the aria tree at root.
func OpenXwalStore(root string) (*XwalStore, error) {
	if root == "" {
		return nil, fmt.Errorf("xwal store: empty root")
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, err
	}
	s := &XwalStore{root: root, cfg: storeConfig(), now: func() int64 { return time.Now().UnixMilli() }}
	if err := s.loadIndex(); err != nil {
		return nil, err
	}
	if err := s.ensureNull(); err != nil {
		return nil, err
	}
	return s, nil
}

// OpenNode opens the xwal for an aria id. Caller closes it.
func (s *XwalStore) OpenNode(id string) (*xwal.XWAL, error) {
	s.mu.Lock()
	rec := s.idx.Nodes[id]
	s.mu.Unlock()
	if rec == nil {
		return nil, fmt.Errorf("xwal store: unknown aria %q", id)
	}
	return xwal.Open(s.root, s.cfg, rec.Branch...)
}

// CreateLoadout returns the node id for (name, content-version-of-patch),
// materializing it as a fork of null if it does not exist yet.
func (s *XwalStore) CreateLoadout(name string, patch message.Patch) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ver, err := contentVersion(patch)
	if err != nil {
		return "", err
	}
	key := name + "@" + ver
	if id := s.idx.Loadouts[key]; id != "" {
		return id, nil
	}
	id := newID()
	stamped := stampLoadout(patch, name, ver)
	// The loadout node's birth tic is renderable (RoleUser, empty
	// content): its chalkboard patch renders as the loadout's
	// <system-reminder> blocks ONCE in this shared prefix, and every
	// conversation forked from it inherits that cached, rendered prefix.
	if err := s.forkChildLocked(NullAriaID, id, kindLoadout, &stamped, message.RoleUser); err != nil {
		return "", err
	}
	rec := s.idx.Nodes[id]
	rec.Loadout, rec.Version = name, ver
	s.idx.Loadouts[key] = id
	return id, s.saveIndex()
}

// CreateConversation forks a loadout node into a new conversation.
func (s *XwalStore) CreateConversation(loadoutID string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ld := s.idx.Nodes[loadoutID]
	if ld == nil || ld.Kind != kindLoadout {
		return "", fmt.Errorf("xwal store: %q is not a loadout node", loadoutID)
	}
	id := newID()
	// The conversation's birth tic is a filtered genesis: it inherits
	// the loadout's rendered prefix via the fork watermark and adds no
	// transition of its own (runtime fill-ins are written by the caller).
	if err := s.forkChildLocked(loadoutID, id, kindConversation, nil, message.RoleGenesis); err != nil {
		return "", err
	}
	return id, s.saveIndex()
}

// Fork branches a conversation at its current head (fork the present).
// See ForkAt.
func (s *XwalStore) Fork(id string) (cont, alt string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec := s.idx.Nodes[id]
	if rec == nil {
		return "", "", fmt.Errorf("xwal store: unknown aria %q", id)
	}
	x, err := xwal.Open(s.root, s.cfg, rec.Branch...)
	if err != nil {
		return "", "", err
	}
	tail := irLast(x)
	x.Close()
	return s.forkAtLocked(rec, tail+1)
}

// ForkAt branches a node at main-LT atMainLT. The node freezes and keeps
// its id as a closed index node; BOTH continuations get fresh ids — the
// original continuation and the new alternative. For an interior fork the
// original suffix becomes the continuation; for a head fork both children
// start empty from the head, sharing the frozen prefix.
func (s *XwalStore) ForkAt(id string, atMainLT uint64) (cont, alt string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec := s.idx.Nodes[id]
	if rec == nil {
		return "", "", fmt.Errorf("xwal store: unknown aria %q", id)
	}
	return s.forkAtLocked(rec, atMainLT)
}

func (s *XwalStore) forkAtLocked(rec *nodeRec, atMainLT uint64) (string, string, error) {
	if rec.Frozen {
		return "", "", fmt.Errorf("xwal store: %q is already a frozen node", rec.ID)
	}
	contID, altID := newID(), newID()

	// First fork: the alternative child, naming the old-future (if any)
	// as the continuation. An interior fork re-homes the original suffix
	// into contID; a head fork creates no old-future. The alternative is
	// always a fresh empty child, so seed it on the handle Fork returns
	// (writing the genesis on a re-opened head-fork branch does not
	// persist to the main channel — only the live fork handle does).
	x, err := xwal.Open(s.root, s.cfg, rec.Branch...)
	if err != nil {
		return "", "", err
	}
	altX, err := x.Fork(atMainLT, altID, contID)
	x.Close()
	if err != nil {
		return "", "", fmt.Errorf("xwal store: fork %q: %w", rec.ID, err)
	}
	if err := s.seedChildHandle(altX); err != nil {
		altX.Close()
		return "", "", fmt.Errorf("xwal store: seed alt %q: %w", altID, err)
	}
	altX.Close()

	// Head fork: no old-future was created, so spawn the continuation as
	// an N-ary sibling of the now-frozen node and seed it. An interior
	// fork's continuation is the re-homed old-future — it already owns the
	// suffix entries, so it needs no seed.
	if !s.irBranchExists(rec, contID) {
		fx, err := xwal.Open(s.root, s.cfg, rec.Branch...)
		if err != nil {
			return "", "", err
		}
		cX, err := fx.Fork(atMainLT, contID, "_x")
		fx.Close()
		if err != nil {
			return "", "", fmt.Errorf("xwal store: continuation fork %q: %w", rec.ID, err)
		}
		if err := s.seedChildHandle(cX); err != nil {
			cX.Close()
			return "", "", fmt.Errorf("xwal store: seed continuation %q: %w", contID, err)
		}
		cX.Close()
	}

	now := s.now()
	// Continuation inherits the parent's trunk (same thread); the
	// alternative founds a new trunk. Vector appends 0 for the
	// continuation, 1 for the alternative.
	children := []struct {
		id    string
		trunk string
		index int
	}{
		{contID, rec.Trunk, 0},
		{altID, altID, 1},
	}
	for _, c := range children {
		s.idx.Nodes[c.id] = &nodeRec{
			ID:        c.id,
			Branch:    append(append([]string(nil), rec.Branch...), c.id),
			Parent:    rec.ID,
			Kind:      kindConversation,
			CreatedMS: now,
			LastMS:    now,
			Trunk:     c.trunk,
			Vector:    append(append([]int(nil), rec.Vector...), c.index),
		}
	}
	rec.Children = append(rec.Children, contID, altID)
	rec.Frozen = true
	return contID, altID, s.saveIndex()
}

// seedChildHandle gives a freshly-forked child its OWN genesis IR tic plus
// an empty chalkboard patch keyed to that tic, written on the live fork
// handle (the one Fork returned). This mirrors forkChildLocked, which works
// — appending the genesis on a *re-opened* head-fork branch does not
// persist to the main channel, leaving the IR own-log empty. The genesis
// matters for re-forkability: it advances the child's own tail so the
// chalkboard seed and the next fork point don't collide at the channel's
// own base index (which figwal refuses to fork at). The genesis is filtered
// from rendering.
func (s *XwalStore) seedChildHandle(x *xwal.XWAL) error {
	gen, _ := json.Marshal(message.Message{Role: message.RoleGenesis, Timestamp: s.now()})
	glt, err := x.AppendMain(gen, nil)
	if err != nil {
		return err
	}
	pb, _ := json.Marshal(message.Patch{})
	_, err = x.Append(chanChalkboard, glt, pb, nil)
	return err
}

// irBranchExists reports whether a child branch dir exists in the IR
// channel for a node — used to tell an interior fork (old-future made)
// from a head fork (none).
func (s *XwalStore) irBranchExists(parent *nodeRec, name string) bool {
	parts := append([]string{s.root, chanIR}, parent.Branch...)
	parts = append(parts, name)
	_, err := os.Stat(filepath.Join(parts...))
	return err == nil
}

// forkChildLocked forks parent into a new child node, writes the child's
// genesis tic, and applies an optional chalkboard patch (loadout stamp).
// The parent becomes a frozen branch point. Caller holds s.mu and saves.
func (s *XwalStore) forkChildLocked(parentID, childID string, kind nodeKind, cbPatch *message.Patch, birthRole message.Role) error {
	parent := s.idx.Nodes[parentID]
	if parent == nil {
		return fmt.Errorf("xwal store: unknown parent %q", parentID)
	}
	px, err := xwal.Open(s.root, s.cfg, parent.Branch...)
	if err != nil {
		return err
	}
	atMainLT := irLast(px) + 1
	child, err := px.Fork(atMainLT, childID, contName)
	px.Close()
	if err != nil {
		return fmt.Errorf("xwal store: fork %s -> %s: %w", parentID, childID, err)
	}
	defer child.Close()

	gen, _ := json.Marshal(message.Message{Role: birthRole, Timestamp: s.now()})
	glt, err := child.AppendMain(gen, nil)
	if err != nil {
		return err
	}
	// Always write a chalkboard genesis entry (empty patch if no loadout
	// stamp), so the child's OWN chalkboard log is non-empty and can later
	// be forked — figwal won't fork an empty log even with a parent.
	patch := message.Patch{}
	if cbPatch != nil {
		patch = *cbPatch
	}
	pb, _ := json.Marshal(patch)
	if _, err := child.Append(chanChalkboard, glt, pb, nil); err != nil {
		return err
	}

	now := s.now()
	childRec := &nodeRec{
		ID:        childID,
		Branch:    append(append([]string(nil), parent.Branch...), childID),
		Parent:    parentID,
		Kind:      kind,
		CreatedMS: now,
		LastMS:    now,
	}
	// A root conversation (fork of a loadout) founds its own trunk and
	// takes the next root slot in the forest vector.
	if kind == kindConversation {
		childRec.Trunk = childID
		childRec.Vector = []int{s.rootCountLocked()}
	}
	s.idx.Nodes[childID] = childRec
	parent.Children = append(parent.Children, childID)
	parent.Frozen = true
	return nil
}

// rootCountLocked counts existing root conversations (Vector length 1) —
// the next root conversation's leading vector component.
func (s *XwalStore) rootCountLocked() int {
	n := 0
	for _, r := range s.idx.Nodes {
		if r.Kind == kindConversation && len(r.Vector) == 1 {
			n++
		}
	}
	return n
}

// irLast returns the IR channel's last index for an open node.
func irLast(x *xwal.XWAL) uint64 {
	for _, c := range x.Channels() {
		if c.Name == chanIR {
			return c.Last
		}
	}
	return 0
}

// contentVersion is the value-stable content hash of a loadout patch,
// using figwal's hash scheme (same as the JSONL _hash sidecar).
func contentVersion(patch message.Patch) (string, error) {
	body, err := json.Marshal(patch)
	if err != nil {
		return "", err
	}
	return segment.ValueHash(body)
}

func stampLoadout(p message.Patch, name, ver string) message.Patch {
	set := make(map[string]json.RawMessage, len(p.Set)+2)
	for k, v := range p.Set {
		set[k] = v
	}
	nb, _ := json.Marshal(name)
	vb, _ := json.Marshal(ver)
	set[keyLoadoutName] = nb
	set[keyLoadoutVer] = vb
	return message.Patch{Set: set, Remove: p.Remove}
}

// ensureNull creates the root null node if absent: genesis tic + an
// (empty) root-default chalkboard base, then recorded in the index.
func (s *XwalStore) ensureNull() error {
	if s.idx.Nodes[NullAriaID] != nil {
		return nil
	}
	x, err := xwal.Open(s.root, s.cfg)
	if err != nil {
		return err
	}
	defer x.Close()
	gen, _ := json.Marshal(message.Message{Role: message.RoleGenesis, Timestamp: s.now()})
	glt, err := x.AppendMain(gen, nil)
	if err != nil {
		return err
	}
	base, _ := json.Marshal(message.Patch{}) // root defaults (empty for now)
	if _, err := x.Append(chanChalkboard, glt, base, nil); err != nil {
		return err
	}
	now := s.now()
	s.idx.Nodes[NullAriaID] = &nodeRec{ID: NullAriaID, Kind: kindNull, CreatedMS: now}
	return s.saveIndex()
}

func (s *XwalStore) loadIndex() error {
	data, err := os.ReadFile(filepath.Join(s.root, indexFile))
	if os.IsNotExist(err) {
		s.idx = &nodeIndex{Nodes: map[string]*nodeRec{}, Loadouts: map[string]string{}}
		return nil
	}
	if err != nil {
		return err
	}
	var idx nodeIndex
	if err := json.Unmarshal(data, &idx); err != nil {
		return fmt.Errorf("xwal store: parse index: %w", err)
	}
	if idx.Nodes == nil {
		idx.Nodes = map[string]*nodeRec{}
	}
	if idx.Loadouts == nil {
		idx.Loadouts = map[string]string{}
	}
	s.idx = &idx
	return nil
}

func (s *XwalStore) saveIndex() error {
	body, _ := json.MarshalIndent(s.idx, "", "  ")
	final := filepath.Join(s.root, indexFile)
	tmp := final + ".tmp"
	if err := os.WriteFile(tmp, body, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, final)
}

func newID() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// NodeView is a read-only snapshot of a tree node (for listing/lineage).
type NodeView struct {
	ID        string
	Parent    string
	Kind      string
	Loadout   string
	Version   string
	Children  []string
	Frozen    bool
	Depth     int
	Trunk     string
	Vector    []int
	CreatedMS int64
	LastMS    int64
}

func (s *XwalStore) view(r *nodeRec) NodeView {
	depth := 0
	for p := r.Parent; p != ""; p = s.idx.Nodes[p].Parent {
		depth++
		if s.idx.Nodes[p] == nil {
			break
		}
	}
	return NodeView{
		ID: r.ID, Parent: r.Parent, Kind: string(r.Kind), Loadout: r.Loadout,
		Version: r.Version, Children: append([]string(nil), r.Children...),
		Frozen: r.Frozen, Depth: depth, Trunk: r.Trunk,
		Vector: append([]int(nil), r.Vector...), CreatedMS: r.CreatedMS, LastMS: r.LastMS,
	}
}

// Nodes returns a view of every node in the tree.
func (s *XwalStore) Nodes() []NodeView {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]NodeView, 0, len(s.idx.Nodes))
	for _, r := range s.idx.Nodes {
		out = append(out, s.view(r))
	}
	return out
}

// Node returns a single node view.
func (s *XwalStore) Node(id string) (NodeView, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.idx.Nodes[id]
	if r == nil {
		return NodeView{}, false
	}
	return s.view(r), true
}

// Touch records last-activity on a node.
func (s *XwalStore) Touch(id string, ms int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r := s.idx.Nodes[id]; r != nil {
		r.LastMS = ms
		_ = s.saveIndex()
	}
}

// RemoveLeaf deletes a childless node and its on-disk branch. The null
// root and nodes with children are refused (delete children first).
func (s *XwalStore) RemoveLeaf(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.idx.Nodes[id]
	if r == nil {
		return fmt.Errorf("xwal store: unknown aria %q", id)
	}
	if r.Kind == kindNull {
		return fmt.Errorf("xwal store: cannot remove the null root")
	}
	if len(r.Children) > 0 {
		return fmt.Errorf("xwal store: %q has children; remove them first", id)
	}
	removeBranchDirs(s.root, r.Branch)
	if p := s.idx.Nodes[r.Parent]; p != nil {
		p.Children = removeStr(p.Children, id)
	}
	if r.Kind == kindLoadout {
		delete(s.idx.Loadouts, r.Loadout+"@"+r.Version)
	}
	delete(s.idx.Nodes, id)
	return s.saveIndex()
}

func removeBranchDirs(root string, branch []string) {
	if len(branch) == 0 {
		return
	}
	sub := filepath.Join(branch...)
	for _, ch := range []string{chanIR, chanChalkboard} {
		_ = os.RemoveAll(filepath.Join(root, ch, sub))
	}
	provs, _ := filepath.Glob(filepath.Join(root, "translations", "*"))
	for _, p := range provs {
		_ = os.RemoveAll(filepath.Join(p, sub))
	}
}

func removeStr(ss []string, x string) []string {
	out := ss[:0]
	for _, s := range ss {
		if s != x {
			out = append(out, s)
		}
	}
	return out
}
