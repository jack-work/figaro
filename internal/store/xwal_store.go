package store

// XwalStore is figaro's aria tree, now a thin policy layer over figwal's
// xwal.Trunks (which owns the fork/trunk mechanics on disk). figaro keeps
// only policy:
//
//	null (root trunk) ──SpawnChild──> loadout trunk ──SpawnChild──> conversation
//	                                                  ──ForkTail/interior fork──> branch…
//
//   - null: the root trunk (xwal.CreateTrunks genesis). Ceremonial, closed.
//   - loadout: SpawnChild(null) + a renderable RoleUser birth message carrying
//     the loadout's chalkboard stamp (system.loadout_name/version). One per
//     (name, content-version); deduped in a small policy side-file. Closed.
//   - conversation: SpawnChild(loadout) — inherits the loadout's rendered
//     prefix via the fork watermark. A live trunk.
//
// The aria id IS the trunk id (stable across forks — the continuation keeps
// it). Trunk identity, the node tree, and fork mechanics live on disk in
// figwal; figaro persists only the null trunk id + the loadout dedup map.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/jack-work/figaro/internal/chalkboard"
	"github.com/jack-work/figaro/internal/message"
	"github.com/jack-work/figwal/segment"
	"github.com/jack-work/figwal/xwal"
)

const (
	chanIR           = "ir"
	chanChalkboard   = "chalkboard"
	reducerJSONMerge = "jsonmerge"
	policyFile       = "policy.json"

	keyLoadoutName = "system.loadout_name"
	keyLoadoutVer  = "system.loadout_version"
)

type nodeKind string

const (
	kindNull         nodeKind = "null"
	kindLoadout      nodeKind = "loadout"
	kindConversation nodeKind = "conversation"
)

// chalkboardReduce folds a message.Patch (JSON) onto a chalkboard
// snapshot (JSON state) — figaro's reducer for the chalkboard channel.
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
	// The root trunk's genesis is a figaro RoleGenesis message (filtered
	// from rendering/context) — not figwal's generic marker, which would
	// read back as an empty-role message in the IR.
	genesis, _ := json.Marshal(message.Message{Role: message.RoleGenesis})
	return xwal.Config{
		Main:    chanIR,
		Codec:   "jsonl",
		Genesis: genesis,
		Registry: map[string]xwal.Reducer{
			reducerJSONMerge: {Reduce: chalkboardReduce, Initial: []byte("{}")},
		},
		Channels: []xwal.ChannelSpec{
			{Name: chanIR, Kind: xwal.ChannelLog},
			{Name: chanChalkboard, Kind: xwal.ChannelReducible, Reducer: reducerJSONMerge},
		},
	}
}

// policy is figaro's side-state: the only things not derivable from the
// figwal trunk tree (the null trunk id and the loadout dedup map).
type policy struct {
	Null     string            `json:"null"`
	Loadouts map[string]string `json:"loadouts"` // "name@version" -> trunk id
}

// XwalStore owns the aria tree (policy over xwal.Trunks).
type XwalStore struct {
	root   string
	cfg    xwal.Config
	mu     sync.Mutex
	trunks *xwal.Trunks
	pol    policy
	now    func() int64
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
	if err := s.loadPolicy(); err != nil {
		return nil, err
	}
	tr, err := xwal.OpenTrunks(root, s.cfg)
	if err != nil {
		// Not initialized yet: create the genesis root trunk (the null).
		tr, nullID, cerr := xwal.CreateTrunks(root, s.cfg)
		if cerr != nil {
			return nil, cerr
		}
		s.trunks = tr
		s.pol.Null = nullID
		if err := s.savePolicy(); err != nil {
			return nil, err
		}
		return s, nil
	}
	s.trunks = tr
	return s, nil
}

// OpenNode opens the xwal for an aria id (the trunk's live head). Caller
// closes it.
func (s *XwalStore) OpenNode(id string) (*xwal.XWAL, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.trunks.Head(id)
}

// CreateLoadout returns the trunk id for (name, content-version-of-patch),
// materializing it as a SpawnChild of null if it does not exist yet.
func (s *XwalStore) CreateLoadout(name string, patch message.Patch) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ver, err := contentVersion(patch)
	if err != nil {
		return "", err
	}
	key := name + "@" + ver
	if id := s.pol.Loadouts[key]; id != "" {
		return id, nil
	}
	id, err := s.trunks.SpawnChild(s.pol.Null)
	if err != nil {
		return "", fmt.Errorf("xwal store: spawn loadout: %w", err)
	}
	// The loadout's birth message is renderable (RoleUser, empty content): its
	// chalkboard patch renders as the loadout's <system-reminder> blocks
	// ONCE in this shared prefix, inherited (cached) by every conversation.
	stamped := stampLoadout(patch, name, ver)
	if err := s.writeBirth(id, message.RoleUser, &stamped); err != nil {
		return "", err
	}
	s.pol.Loadouts[key] = id
	return id, s.savePolicy()
}

// CreateConversation spawns a conversation from a loadout trunk.
func (s *XwalStore) CreateConversation(loadoutID string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.kindOf(loadoutID) != kindLoadout {
		return "", fmt.Errorf("xwal store: %q is not a loadout", loadoutID)
	}
	id, err := s.trunks.SpawnChild(loadoutID)
	if err != nil {
		return "", fmt.Errorf("xwal store: spawn conversation: %w", err)
	}
	// No birth message: the conversation inherits the loadout's rendered prefix
	// via the fork watermark; its own IR starts empty (first turn appends).
	return id, nil
}

// Fork branches a conversation at its head. The aria id is STABLE — the
// trunk continues under the same id (cont == id); only the alternative is
// new. (bind-to-trunk: forking your trunk doesn't move you.) A cauterized
// trunk (null/loadout) never continues — a fork there mints a fresh child
// trunk beneath it (a loadoutless conversation, or one sharing the loadout).
func (s *XwalStore) Fork(id string) (cont, alt string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cauterized(id) {
		alt, err = s.trunks.SpawnChild(id)
	} else {
		alt, err = s.trunks.ForkTail(id)
	}
	if err != nil {
		return "", "", err
	}
	return id, alt, nil
}

// ForkAt branches at an interior main-LT (imperative — no message): shares
// [1..atMainLT], mints an empty alternative diverging at atMainLT+1; the id
// is stable (cont == id). At/past the tail it degenerates to a tail fork.
//
// Cauterization: if atMainLT is owned by a ceremonial trunk (the null root or
// a loadout), it is NOT re-split into a continuation — a new child trunk is
// spawned beneath the owner (a new loadoutless conversation, or a new
// conversation sharing that loadout). Forking a conversation's own turns (or a
// parent conversation's) re-splits normally.
func (s *XwalStore) ForkAt(id string, atMainLT uint64) (cont, alt string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	owner, oerr := s.trunks.OwnerTrunk(id, atMainLT)
	if oerr != nil {
		return "", "", oerr
	}
	if s.cauterized(owner) {
		alt, err = s.trunks.SpawnChild(owner)
	} else {
		alt, err = s.trunks.ForkAt(id, atMainLT)
	}
	if err != nil {
		return "", "", err
	}
	return id, alt, nil
}

// cauterized reports whether a trunk is ceremonial (the null root or a
// loadout) — ops "at" it spawn a child trunk rather than appending/re-splitting.
func (s *XwalStore) cauterized(id string) bool {
	k := s.kindOf(id)
	return k == kindNull || k == kindLoadout
}

// writeBirth appends a birth message to a fresh trunk's IR plus its chalkboard
// patch (the loadout stamp, or empty). Caller holds s.mu.
func (s *XwalStore) writeBirth(id string, role message.Role, cbPatch *message.Patch) error {
	x, err := s.trunks.Head(id)
	if err != nil {
		return err
	}
	defer x.Close()
	gen, _ := json.Marshal(message.Message{Role: role, Timestamp: s.now()})
	glt, err := x.AppendMain(gen, nil)
	if err != nil {
		return err
	}
	patch := message.Patch{}
	if cbPatch != nil {
		patch = *cbPatch
	}
	pb, _ := json.Marshal(patch)
	_, err = x.Append(chanChalkboard, glt, pb, nil)
	return err
}

// contentVersion is the value-stable content hash of a loadout patch.
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

// kindOf derives a trunk's kind from its lineage (no stored kind):
// the null root, a child of null (loadout), else a conversation.
func (s *XwalStore) kindOf(id string) nodeKind {
	if id == s.pol.Null {
		return kindNull
	}
	for _, t := range s.trunks.List() {
		if t.ID == id {
			if t.Parent == s.pol.Null {
				return kindLoadout
			}
			return kindConversation
		}
	}
	// Closed trunks (loadouts) aren't in List(); a known loadout id is one.
	for _, lid := range s.pol.Loadouts {
		if lid == id {
			return kindLoadout
		}
	}
	return ""
}

// NodeView is a read-only snapshot of an aria (trunk) for listing/lineage.
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

// view renders a live (conversation) trunk. Kind is always conversation —
// closed ceremonial trunks (null/loadout) aren't in List().
func (s *XwalStore) view(t xwal.TrunkInfo, vec map[string][]int) NodeView {
	return NodeView{
		ID: t.ID, Parent: t.Parent, Kind: string(kindConversation), Trunk: t.ID, Vector: vec[t.ID],
	}
}

// vectorsLocked assigns each conversation trunk its fork-forest vector: the
// child-index path among conversation trunks — roots are [0],[1],…, a branch
// is parentVec+[k] — ordered by trunk number (creation order). Caller holds mu.
func (s *XwalStore) vectorsLocked() map[string][]int {
	infos := s.trunks.List()
	live := make(map[string]bool, len(infos))
	for _, ti := range infos {
		live[ti.ID] = true
	}
	kids := map[string][]string{}
	var roots []string
	for _, ti := range infos {
		if ti.Parent != "" && live[ti.Parent] {
			kids[ti.Parent] = append(kids[ti.Parent], ti.ID) // branch of a conversation
		} else {
			roots = append(roots, ti.ID) // root conversation (parent is a loadout/null)
		}
	}
	byNum := func(ids []string) {
		sort.Slice(ids, func(i, j int) bool { return trunkNum(ids[i]) < trunkNum(ids[j]) })
	}
	byNum(roots)
	for k := range kids {
		byNum(kids[k])
	}
	vec := map[string][]int{}
	var assign func(id string, prefix []int)
	assign = func(id string, prefix []int) {
		vec[id] = prefix
		for i, c := range kids[id] {
			assign(c, append(append([]int(nil), prefix...), i))
		}
	}
	for i, r := range roots {
		assign(r, []int{i})
	}
	return vec
}

func trunkNum(id string) int {
	if len(id) < 2 || id[0] != 't' {
		return 1 << 30
	}
	n, err := strconv.Atoi(id[1:])
	if err != nil {
		return 1 << 30
	}
	return n
}

// Nodes returns a view of every conversation trunk plus the loadout/null
// infrastructure trunks.
func (s *XwalStore) Nodes() []NodeView {
	s.mu.Lock()
	defer s.mu.Unlock()
	vec := s.vectorsLocked()
	out := make([]NodeView, 0)
	for _, t := range s.trunks.List() {
		out = append(out, s.view(t, vec))
	}
	// Closed ceremonial trunks (null + loadouts) aren't in List().
	out = append(out, NodeView{ID: s.pol.Null, Kind: string(kindNull), Trunk: s.pol.Null})
	for key, lid := range s.pol.Loadouts {
		name, ver := splitLoadoutKey(key)
		out = append(out, NodeView{ID: lid, Kind: string(kindLoadout), Trunk: lid, Parent: s.pol.Null, Loadout: name, Version: ver})
	}
	return out
}

// Node returns a single trunk view (incl. closed loadout/null trunks).
func (s *XwalStore) Node(id string) (NodeView, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if id == s.pol.Null {
		return NodeView{ID: id, Kind: string(kindNull), Trunk: id}, true
	}
	for key, lid := range s.pol.Loadouts {
		if lid == id {
			name, ver := splitLoadoutKey(key)
			return NodeView{ID: id, Kind: string(kindLoadout), Trunk: id, Parent: s.pol.Null, Loadout: name, Version: ver}, true
		}
	}
	for _, t := range s.trunks.List() {
		if t.ID == id {
			return s.view(t, s.vectorsLocked()), true
		}
	}
	return NodeView{}, false
}

func splitLoadoutKey(key string) (name, ver string) {
	for i := len(key) - 1; i >= 0; i-- {
		if key[i] == '@' {
			return key[:i], key[i+1:]
		}
	}
	return key, ""
}

// Touch is a no-op now: list recency comes from the per-aria meta sidecar.
func (s *XwalStore) Touch(id string, ms int64) {}

// RemoveLeaf deletes a trunk (its subtree) via xwal.Trunks. Trunk-addressed;
// refuses a trunk with live branches unless recursive.
func (s *XwalStore) RemoveLeaf(id string, recursive bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.trunks.Remove(id, recursive)
}

func (s *XwalStore) loadPolicy() error {
	data, err := os.ReadFile(filepath.Join(s.root, policyFile))
	if os.IsNotExist(err) {
		s.pol = policy{Loadouts: map[string]string{}}
		return nil
	}
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, &s.pol); err != nil {
		return fmt.Errorf("xwal store: parse policy: %w", err)
	}
	if s.pol.Loadouts == nil {
		s.pol.Loadouts = map[string]string{}
	}
	return nil
}

func (s *XwalStore) savePolicy() error {
	body, _ := json.MarshalIndent(s.pol, "", "  ")
	final := filepath.Join(s.root, policyFile)
	tmp := final + ".tmp"
	if err := os.WriteFile(tmp, body, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, final)
}
