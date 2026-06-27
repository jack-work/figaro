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
	if err := s.forkChildLocked(NullAriaID, id, kindLoadout, &stamped); err != nil {
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
	if err := s.forkChildLocked(loadoutID, id, kindConversation, nil); err != nil {
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
	// into contID; a head fork creates no old-future.
	x, err := xwal.Open(s.root, s.cfg, rec.Branch...)
	if err != nil {
		return "", "", err
	}
	altX, err := x.Fork(atMainLT, altID, contID)
	x.Close()
	if err != nil {
		return "", "", fmt.Errorf("xwal store: fork %q: %w", rec.ID, err)
	}
	altX.Close()

	// Head fork: no old-future was created, so spawn the continuation as
	// an N-ary sibling of the now-frozen node.
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
		cX.Close()
	}

	now := s.now()
	for _, cid := range []string{contID, altID} {
		s.idx.Nodes[cid] = &nodeRec{
			ID:        cid,
			Branch:    append(append([]string(nil), rec.Branch...), cid),
			Parent:    rec.ID,
			Kind:      kindConversation,
			CreatedMS: now,
			LastMS:    now,
		}
		if err := s.seedChalkboard(cid); err != nil {
			return "", "", err
		}
	}
	rec.Children = append(rec.Children, contID, altID)
	rec.Frozen = true
	return contID, altID, s.saveIndex()
}

// seedChalkboard writes an empty chalkboard patch to a freshly-forked
// child so its OWN chalkboard log is non-empty and re-forkable (a head
// fork inherits chalkboard via watermark but owns no entries). The empty
// patch is a no-op fold and renders nothing.
func (s *XwalStore) seedChalkboard(childID string) error {
	rec := s.idx.Nodes[childID]
	x, err := xwal.Open(s.root, s.cfg, rec.Branch...)
	if err != nil {
		return err
	}
	defer x.Close()
	pb, _ := json.Marshal(message.Patch{})
	_, err = x.Append(chanChalkboard, irLast(x)+1, pb, nil)
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
func (s *XwalStore) forkChildLocked(parentID, childID string, kind nodeKind, cbPatch *message.Patch) error {
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

	gen, _ := json.Marshal(message.Message{Role: message.RoleGenesis})
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
	s.idx.Nodes[childID] = &nodeRec{
		ID:        childID,
		Branch:    append(append([]string(nil), parent.Branch...), childID),
		Parent:    parentID,
		Kind:      kind,
		CreatedMS: now,
		LastMS:    now,
	}
	parent.Children = append(parent.Children, childID)
	parent.Frozen = true
	return nil
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
	gen, _ := json.Marshal(message.Message{Role: message.RoleGenesis})
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
