package store

// XwalStore is figaro's aria tree, a thin policy layer over figwal's
// xwal.Trunks (which owns the fork/trunk mechanics on disk). figaro keeps
// only policy:
//
//	root (null) ──CreateStump──> loadout (stump) ──SpawnUnderStump──> conversation
//	                                                ──ForkTail/interior fork──> branch…
//
//   - root: the channel dir itself (xwal.CreateTrunks genesis). Markerless,
//     ceremonial — the "null" anchor. Addressed by the rootID sentinel.
//   - loadout: a markerless, named stump (CreateStump) holding a renderable
//     RoleInput birth message that carries the loadout's chalkboard stamp
//     (system.loadout_name/version). One per (name, content-version); the
//     stump NAME is "<name>@<content-version>", so the dedup map lives on
//     disk (Stumps()) — no policy side-file. Ceremonial.
//   - conversation: SpawnUnderStump(loadout) — inherits the loadout's
//     rendered prefix via the fork watermark. A live trunk.
//
// The aria id IS the trunk id (stable across forks — the continuation keeps
// it). Trunk identity, the node tree, and fork mechanics live on disk in
// figwal; figaro derives loadouts/null from the stump/root structure.

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jack-work/figaro/internal/chalkboard"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/message"
	"github.com/jack-work/figaro/internal/topo"
	"github.com/jack-work/figwal/segment"
	"github.com/jack-work/figwal/xwal"
)

// trunkScanCount counts calls into figwal's trunk-listing accessors
// (Trunks.ListLight + Trunks.Stumps). It is the proxy the benchmark asserts
// on to catch a fan-out regression (a listing that rescans the forest N times
// instead of once). ListLight itself no longer opens trunk heads.
var trunkScanCount atomic.Int64

// listTrunks / listStumps wrap the figwal accessors so every forest scan is
// counted. Always go through these inside the store.
func (s *XwalStore) listTrunks() []xwal.TrunkInfo {
	trunkScanCount.Add(1)
	// ListLight, not List: figaro never uses TrunkInfo.Tip, and List opens
	// every trunk's head (a segment scan) just to compute it. ListLight is
	// all in-memory + a cheap .fork read — the difference is `fig ls` at
	// ~300ms vs ~tens of ms on a store with many/large arias.
	return s.trunks.ListLight()
}

func (s *XwalStore) listStumps() []xwal.StumpInfo {
	trunkScanCount.Add(1)
	return s.trunks.Stumps()
}

// hexTrunkID mints an opaque aria/trunk id (the same 4-byte hex form figaro
// has always used for aria handles), so conversation ids read like real
// handles rather than sequential "t<N>".
func hexTrunkID() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

const (
	chanIR         = "ir"
	chanChalkboard = "chalkboard"
	// chanUI is the derived turn-shaped UI IR cache (Phase 4). Declared here
	// so the schema registry can version it before it carries data.
	chanUI = "ui"

	keyLoadoutName = "system.loadout_name"
	keyLoadoutVer  = "system.loadout_version"

	// rootID is the ceremonial "null" anchor's display id. The root is the
	// channel dir itself — it carries no trunk id on disk — so figaro names
	// it with a stable sentinel for listing/lineage.
	rootID = "null"
)

type nodeKind string

const (
	kindNull         nodeKind = "null"
	kindLoadout      nodeKind = "loadout"
	kindConversation nodeKind = "conversation"
)

// chalkboardReduce folds a message.Patch (JSON) onto a chalkboard
// snapshot (JSON state) — figaro's reducer for the chalkboard channel.
//
// The Snapshot's MarshalJSON/UnmarshalJSON are called DIRECTLY rather than
// through json.Marshal/json.Unmarshal, and that is not a style tic: for a
// type with custom JSON hooks, encoding/json pre-scans the input before
// handing it to an Unmarshaler and re-scans a Marshaler's output before
// emitting it. On a 15KB board each of those doubles the cost (measured:
// 97µs -> 188µs decode, 76µs -> 152µs encode), and this reducer runs once
// per WAL record on segment rollover and fork. The bytes are identical
// either way — TestSnapshotDirectCodecMatchesEncodingJSON pins that.
func chalkboardReduce(state, patch []byte) ([]byte, error) {
	snap := chalkboard.Snapshot{}
	if len(state) > 0 {
		if err := snap.UnmarshalJSON(state); err != nil {
			return nil, err
		}
	}
	var p message.Patch
	if err := json.Unmarshal(patch, &p); err != nil {
		return nil, err
	}
	next := snap.Apply(p)
	return next.MarshalJSON()
}

// segmentSize bounds one WAL segment file. figwal's 64MB default is a
// server-log figure: MEASURED on the author's store (300 segments, 18262 IR
// entries, 29MB — 1.6KB/entry; biggest single segment 1.78MB/1624 entries),
// nothing has ever rolled and nothing ever would, which makes
// SegmentBaseIndexes — the coarse "which file holds LT N" index a lazy read
// wants — a constant function.
//
// 2MiB gives ~1300 entries per segment at the measured density: the largest
// real aria rolls, an ordinary one still fits in one file. The floor on the
// choice is that a single record must fit inside a segment (disk.Log returns
// ErrPayloadTooLarge otherwise). The largest record anywhere in the store is
// 128KB, so 2MiB is 16x headroom; the only unbounded producer is an inlined
// base64 image from the read tool, which is why this is not 1MiB.
//
// It is a default, not a law: `[store] segment_size` overrides it, and
// config.SegmentSize is the single place the number and its floor live. The
// zero passed here means "whatever config says", which for a test or a tool
// that opens a store without config is exactly the default above.
//
// Affects new segments only — existing arias keep their oversized files and
// simply stop growing them.
func storeOptions(segmentSize int) xwal.StoreOptions {
	if segmentSize <= 0 {
		var noConfig *config.Loaded // the accessor is nil-safe on purpose
		segmentSize = noConfig.SegmentSize()
	}
	// The root genesis is a figaro RoleGenesis message (filtered from
	// rendering/context) — not figwal's generic marker, which would read back
	// as an empty-role message in the IR.
	genesis, _ := json.Marshal(message.Message{Role: message.RoleGenesis})
	return xwal.StoreOptions{
		Main:        chanIR,
		Codec:       "jsonl",
		SegmentSize: int64(segmentSize),
		Genesis:     genesis,
		MintTrunkID: hexTrunkID,
		Reducers: map[string]xwal.Reducer{
			chanChalkboard: {Reduce: chalkboardReduce, Initial: []byte("{}")},
		},
		Opaque: []string{
			transChannel("anthropic"),
			transChannel("copilot-messages"),
			transChannel("copilot-responses"),
		},
		// The chalkboard is UNKEYED: a patch is a declaration of intent, not
		// a fact about a turn, so it should not have to read the timeline to
		// be written. That is what lets a `set` land mid-turn.
		//
		// The translations channels stay KEYED, and deliberately: their main
		// LT is a lookup key ("the provider message for turn k"), and a
		// translation is derived AFTER its turn exists, so there is no
		// moment at which the main record could stamp a cursor for it.
		Unkeyed: []string{chanChalkboard},
	}
}

// XwalStore owns the aria tree (policy over xwal.Store, whose flusher
// owns all durability: appends are memory-first, disk follows with
// bounded lag, Kick expedites).
type XwalStore struct {
	root     string
	mu       sync.Mutex
	trunks   *xwal.Store
	topology atomic.Pointer[topologySnapshot]
	now      func() int64
	// tree is the PRESENTATION hierarchy: what fig ls draws and what a
	// delete takes. Never consulted for forking — that reads .from.
	tree topo.Tree
}

// Topology exposes the .from adjacency for topo.Tree and boundary
// computation. It is the only lineage figwal considers authoritative.
type xwalTopology struct{ s *XwalStore }

func (t xwalTopology) From(id string) (string, bool) {
	n, ok := t.s.Node(id)
	if !ok {
		return "", false
	}
	return n.Parent, true
}

func (t xwalTopology) Nodes() []string {
	snap := t.s.topology.Load()
	if snap == nil {
		return nil
	}
	out := make([]string, 0, len(snap.nodes))
	for _, n := range snap.nodes {
		out = append(out, n.ID)
	}
	return out
}

func (t xwalTopology) ChildrenOf(id string) []string {
	var out []string
	for _, n := range t.Nodes() {
		if p, ok := t.From(n); ok && p == id && n != id {
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out
}

// Tree is the presentation hierarchy in force.
func (s *XwalStore) Tree() topo.Tree { return s.tree }

// TopologyAdjacency is the .from lineage, for building a presentation tree
// on top of it.
func (s *XwalStore) TopologyAdjacency() topo.Topology { return xwalTopology{s} }

// SetTree installs a presentation hierarchy. The wiring calls this with a
// trunk capability; without one the store keeps the topology tree.
func (s *XwalStore) SetTree(t topo.Tree) { s.tree = t }

type topologySnapshot struct {
	version         uint64
	nodes           []NodeView
	conversations   []NodeView
	conversationIDs []string
	byID            map[string]NodeView
}

// OpenXwalStore opens the aria tree at root, creating it when absent.
// segmentSize <= 0 takes the configured default (see storeOptions).
func OpenXwalStore(root string, segmentSize int) (*XwalStore, error) {
	if root == "" {
		return nil, fmt.Errorf("xwal store: empty root")
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, err
	}
	// Before the store opens, because an unmigrated store does not open at
	// all -- and before THAT was true, it opened reporting its loadouts and
	// none of its arias. The single writer (the daemon) owns this; the
	// migration takes the store lock itself, so a second process waits for
	// a store rather than half-reading one.
	if err := migrateLayout(root); err != nil {
		return nil, err
	}
	st, err := xwal.OpenStore(root, storeOptions(segmentSize))
	if err != nil {
		return nil, err
	}
	// Single writer (the daemon) owns migration; the CLI never sees it.
	if err := ensureSchema(root, st); err != nil {
		return nil, err
	}
	x := &XwalStore{
		root: root, trunks: st,
		now: func() int64 { return time.Now().UnixMilli() },
	}
	x.tree = topo.FromTopology(xwalTopology{x})
	return x, nil
}

// Close releases the tree.
func (s *XwalStore) Close() error { return s.trunks.Close() }

// migrateLayout brings a store written by an older figaro up to the layout
// this build reads: every node at depth 1, lineage in its own marker. It is
// automatic because the alternative is a daemon that refuses to start until
// the user runs a command he has not heard of -- but it is not silent, and
// it is not partial. Either it finishes or the open fails.
//
// The check costs one file read on a store that needs nothing.
func migrateLayout(root string) error {
	need, err := xwal.NeedsFlatten(root)
	if err != nil || !need {
		return err
	}
	start := time.Now()
	rep, err := xwal.Flatten(root)
	if err != nil {
		return fmt.Errorf("store %s needs a layout migration and it failed: %w", root, err)
	}
	slog.Info("store layout migrated",
		"nodes", rep.Nodes, "moved", rep.Moved, "markers", rep.Markers,
		"retired", rep.Retired, "ms", time.Since(start).Milliseconds())
	return nil
}

// OpenNode opens the xwal for an aria id (the trunk's live head). Caller
// closes it.
func (s *XwalStore) OpenNode(id string) (*xwal.XWAL, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.trunks.Head(id)
}

// loadoutStump is the stump name for a (name, content-version) loadout.
func loadoutStump(name, ver string) string { return name + "@" + ver }

// CreateLoadout returns the loadout id (its stump name) for (name,
// content-version-of-patch), materializing it as a markerless stump under the
// root if it does not exist yet.
func (s *XwalStore) CreateLoadout(name string, patch message.Patch) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ver, err := contentVersion(patch)
	if err != nil {
		return "", err
	}
	stump := loadoutStump(name, ver)
	for _, st := range s.trunks.Stumps() {
		if st.Name == stump {
			return stump, nil // already materialized
		}
	}
	if err := s.trunks.CreateStump(stump); err != nil {
		return "", fmt.Errorf("xwal store: create loadout stump: %w", err)
	}
	// The loadout's birth message is renderable (RoleInput, empty content): its
	// chalkboard patch renders as the loadout's <system-reminder> blocks ONCE
	// in this shared prefix, inherited (cached) by every conversation.
	stamped := stampLoadout(patch, name, ver)
	if err := s.writeStumpBirth(stump, &stamped); err != nil {
		return "", err
	}
	return stump, nil
}

// CreateConversation spawns a conversation from a loadout stump.
func (s *XwalStore) CreateConversation(loadoutID string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id, err := s.trunks.SpawnUnderStump(loadoutID)
	if err != nil {
		return "", fmt.Errorf("xwal store: spawn conversation: %w", err)
	}
	// No birth message: the conversation inherits the loadout's rendered prefix
	// via the fork watermark; its own IR starts empty (first turn appends).
	return id, nil
}

// Fork branches a conversation at its head. The aria id is STABLE — the trunk
// continues under the same id (cont == id); only the alternative is new.
// (bind-to-trunk: forking your trunk doesn't move you.)
func (s *XwalStore) Fork(id string) (cont, alt string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	alt, err = s.trunks.ForkTail(id)
	if err != nil {
		return "", "", err
	}
	return id, alt, nil
}

// ForkAt branches at an interior main-LT (imperative — no message): shares
// [1..atMainLT], mints an empty alternative diverging at atMainLT+1; the id is
// stable (cont == id). At/past the tail it degenerates to a tail fork.
//
// Cauterization: if atMainLT is owned by the root or a loadout stump, it is
// NOT re-split into a continuation — a fresh conversation is spawned beneath
// the owner (a loadoutless conversation under the root, or one sharing that
// loadout). Forking a conversation's own turns (or a parent conversation's)
// re-splits normally.
func (s *XwalStore) ForkAt(id string, atMainLT uint64) (cont, alt string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	owner, oerr := s.trunks.Owner(id, atMainLT)
	if oerr != nil {
		return "", "", oerr
	}
	switch {
	case owner.IsRoot:
		alt, err = s.trunks.SpawnUnderRoot()
	case owner.Stump != "":
		alt, err = s.trunks.SpawnUnderStump(owner.Stump)
	default:
		alt, err = s.trunks.ForkAt(id, atMainLT)
	}
	if err != nil {
		return "", "", err
	}
	return id, alt, nil
}

// Promote raises an aria in the PRESENTATION hierarchy. It edits the trunk
// pstate and writes nothing to any aria's history, so it is O(1) in history
// length. ErrAtStump means there is nothing above to promote into, or the
// build has no trunk capability at all.
func (s *XwalStore) Promote(id string, levels int) (int, error) {
	// NO s.mu here: the tree resolves lineage through Node, which refreshes
	// the topology snapshot under s.mu. Holding it across the tree call
	// deadlocks. The tree carries its own lock and its write is atomic.
	for climbed := 0; climbed < levels; climbed++ {
		if err := s.tree.Promote(id); err != nil {
			if errors.Is(err, topo.ErrNoPromote) {
				return climbed, ErrNoTrunkCapability
			}
			if climbed > 0 {
				return climbed, nil // ran out of levels to climb; not an error
			}
			return 0, ErrAtStump
		}
	}
	return levels, nil
}

// OwnerOf resolves which node owns atMainLT along a trunk's lineage (a trunk,
// a loadout stump, or the root) — for the <trunk>:<LT> addressing announcement.
func (s *XwalStore) OwnerOf(id string, atMainLT uint64) (xwal.Owner, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.trunks.Owner(id, atMainLT)
}

// writeStumpBirth appends a loadout stump's renderable birth message (IR +
// chalkboard stamp). Caller holds s.mu.
func (s *XwalStore) writeStumpBirth(stump string, cbPatch *message.Patch) error {
	x, err := s.trunks.StumpHead(stump)
	if err != nil {
		return err
	}
	defer x.Close()
	patch := message.Patch{}
	if cbPatch != nil {
		patch = *cbPatch
	}
	pb, _ := json.Marshal(patch)

	// THE BOARD PATCH GOES FIRST, and the order is the whole point.
	//
	// A main record carries a CURSOR STAMP: where each unkeyed channel stood
	// when the record was written. The loadout's reminders are meant to
	// render at THIS record -- once, in the prefix every conversation under
	// the stump inherits -- and the projection renders exactly the patches at
	// or below the record's stamp. Writing the record first stamped it one
	// index BELOW the patch it introduces, so PatchesUpTo() returned nothing
	// and no aria created under the stump ever rendered its skills, its credo
	// or anything else the loadout sets.
	//
	// The patch is keyed to the LT the birth record is about to take, which is
	// what it was keyed to before (the record's own LT) and is the reducible
	// one-ahead convention the flush coherence rule already allows. On an
	// unkeyed chalkboard the key is ignored and the stamp is what matters;
	// keying it correctly keeps the keyed case honest rather than relying on
	// the channel happening to be unkeyed.
	next := mainTailOf(x) + 1
	if _, err := x.Append(chanChalkboard, next, pb, nil); err != nil {
		return err
	}
	gen, _ := json.Marshal(message.Message{Role: message.RoleInput, Timestamp: s.now()})
	glt, err := x.AppendMain(gen, nil)
	if err != nil {
		return err
	}
	if glt != next {
		// Nothing else may write to a stump: CreateLoadout holds s.mu and the
		// stump is minted here. If this ever fires, the patch is keyed to a
		// record that does not exist and the reminders would render against
		// the wrong turn.
		return fmt.Errorf("xwal store: stump %s birth record landed at %d, board patch keyed to %d",
			stump, glt, next)
	}
	// Birth records must be durable before conversations spawn under the
	// stump — a crash between spawn and the next flush would orphan the
	// children's fork base.
	return x.SyncCoherent()
}

// mainTailOf is the stump's main channel tail, for keying a record to the
// LT the next append will take.
func mainTailOf(x *xwal.XWAL) uint64 {
	for _, ch := range x.Channels() {
		if ch.Name == chanIR {
			return ch.Last
		}
	}
	return 0
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

// NodeView is a read-only snapshot of an aria (trunk) for listing/lineage.
//
// It carries no `Frozen`/`Children`/`Depth`: those belonged to figaro's own
// pre-trunk forest, where forking froze the target into a read-only index
// node and minted two fresh children. Since the trunk migration the aria id
// is stable — the continuation IS the aria you forked — so no aria is ever
// frozen, and node-level children/depth are figwal's business, not a
// listing's.
type NodeView struct {
	ID         string
	Parent     string
	Kind       string
	Loadout    string
	Version    string
	Trunk      string
	Vector     []int
	BranchedLT uint64 // main-LT this trunk diverged from its parent
}

// view renders a live (conversation) trunk. Its parent for the global
// hierarchy is its loadout stump (top-level) or its parent conversation trunk
// (a branch); a loadoutless top-level trunk hangs off the root.
func (s *XwalStore) view(t xwal.TrunkInfo, vec map[string][]int) NodeView {
	parent := t.Parent
	if parent == "" {
		if t.Stump != "" {
			parent = t.Stump // top-level conversation: nests under its loadout
		} else {
			parent = rootID // loadoutless top-level conversation
		}
	}
	return NodeView{
		ID: t.ID, Parent: parent, Kind: string(kindConversation), Trunk: t.ID,
		Vector: vec[t.ID], BranchedLT: t.BranchedLT,
	}
}

// vectorsLocked assigns each conversation trunk its fork-forest vector: the
// child-index path among conversation trunks — roots are [0],[1],…, a branch
// is parentVec+[k]. Siblings are ordered by id (stable; display re-sorts by
// recency). The trunk list is passed in so callers compute it once per
// request (it costs a full disk scan). Caller holds mu.
func (s *XwalStore) vectorsLocked(infos []xwal.TrunkInfo) map[string][]int {
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
			roots = append(roots, ti.ID) // top-level conversation (parent is a stump/root)
		}
	}
	sort.Strings(roots)
	for k := range kids {
		sort.Strings(kids[k])
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

func (s *XwalStore) topologySnapshot() *topologySnapshot {
	version := s.trunks.Version()
	if snapshot := s.topology.Load(); snapshot != nil && snapshot.version == version {
		return snapshot
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	version = s.trunks.Version()
	if snapshot := s.topology.Load(); snapshot != nil && snapshot.version == version {
		return snapshot
	}

	infos := s.listTrunks()
	vec := s.vectorsLocked(infos)
	conversations := make([]NodeView, 0, len(infos))
	ids := make([]string, 0, len(infos))
	nodes := make([]NodeView, 0, len(infos)+1)
	byID := make(map[string]NodeView, len(infos)+1)
	for _, t := range infos {
		node := s.view(t, vec)
		conversations = append(conversations, node)
		ids = append(ids, node.ID)
		nodes = append(nodes, node)
		byID[node.ID] = node
	}
	root := NodeView{ID: rootID, Kind: string(kindNull), Trunk: rootID}
	nodes = append(nodes, root)
	byID[root.ID] = root
	for _, st := range s.listStumps() {
		name, ver := splitLoadoutKey(st.Name)
		node := NodeView{ID: st.Name, Kind: string(kindLoadout), Parent: rootID, Loadout: name, Version: ver}
		nodes = append(nodes, node)
		byID[node.ID] = node
	}
	snapshot := &topologySnapshot{
		version:         version,
		nodes:           nodes,
		conversations:   conversations,
		conversationIDs: ids,
		byID:            byID,
	}
	s.topology.Store(snapshot)
	return snapshot
}

// Conversations returns a view of every conversation trunk, including
// fork-forest vectors but excluding ceremonial anchors.
func (s *XwalStore) Conversations() []NodeView {
	return append([]NodeView(nil), s.topologySnapshot().conversations...)
}

// ConversationIDs returns persisted conversation ids without computing
// vectors or reading ceremonial loadout anchors.
func (s *XwalStore) ConversationIDs() []string {
	return append([]string(nil), s.topologySnapshot().conversationIDs...)
}

// Nodes returns a view of every conversation trunk plus the ceremonial
// anchors (the root + every loadout stump).
func (s *XwalStore) Nodes() []NodeView {
	return append([]NodeView(nil), s.topologySnapshot().nodes...)
}

// Node returns a single trunk view (incl. the root + loadout stumps).
func (s *XwalStore) Node(id string) (NodeView, bool) {
	node, ok := s.topologySnapshot().byID[id]
	return node, ok
}

func splitLoadoutKey(key string) (name, ver string) {
	for i := len(key) - 1; i >= 0; i-- {
		if key[i] == '@' {
			return key[:i], key[i+1:]
		}
	}
	return key, ""
}

// RemoveLeaf deletes an aria via xwal.Trunks. Trunk-addressed; refuses one
// with live branches unless recursive.
//
// Once an aria has been promoted the two hierarchies diverge, and a delete
// that follows presentation can take a directory some surviving aria still
// inherits its history through. Those survivors absorb the prefix they
// borrow and stop pointing at it BEFORE anything is unlinked, so a crash
// between the two leaves them reading through directories still present.
func (s *XwalStore) RemoveLeaf(id string, recursive bool) error {
	// Repair the boundary FIRST: every survivor that reads its history
	// through this delete set absorbs that prefix and stops pointing at it.
	// Only then does anything get unlinked, so a crash between the two
	// leaves survivors that still read through directories still present.
	for _, orphan := range s.deleteOrphans(id) {
		// Boundary speaks in aria ids; Detach addresses the node directory.
		node, ok := s.trunks.HeadNode(orphan)
		if !ok {
			return fmt.Errorf("%w: no node for %s", ErrWouldOrphan, orphan)
		}
		if err := s.trunks.Detach(node); err != nil {
			return fmt.Errorf("%w: detaching %s: %v", ErrWouldOrphan, orphan, err)
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.trunks.Remove(id, recursive)
}

// Normalize makes every aria independent of the arias it is no longer
// presented under: each one absorbs the history prefix it reads through an
// ancestor. After it, a delete's boundary is empty whatever the
// presentation hierarchy says, so nothing is ever owed at delete time.
//
// This is the DEFERRED work made immediate. It is O(absorbed bytes), so it
// is the one operation here that is not instant; everything else stays so
// precisely because this can be postponed.
func (s *XwalStore) Normalize() (int, error) {
	done := 0
	for _, id := range s.tree.Overridden() {
		node, ok := s.trunks.HeadNode(id)
		if !ok {
			return done, fmt.Errorf("normalize: no node for %s", id)
		}
		if err := s.trunks.Detach(node); err != nil {
			return done, fmt.Errorf("normalize %s: %w", id, err)
		}
		done++
	}
	return done, nil
}

// deleteOrphans is the survivors a delete of id would strand. Empty whenever
// the presentation hierarchy is normalized, which is always without the
// trunk capability.
func (s *XwalStore) deleteOrphans(id string) []string {
	if s.tree.Normalized() {
		return nil
	}
	return topo.Boundary(s.TopologyAdjacency(), s.tree.DeleteSet(id))
}
