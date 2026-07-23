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
//     RoleUser birth message that carries the loadout's chalkboard stamp
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
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jack-work/figaro/internal/chalkboard"
	"github.com/jack-work/figaro/internal/message"
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
	chanIR           = "ir"
	chanChalkboard   = "chalkboard"
	chanTurnWAL      = "turn-wal"
	reducerJSONMerge = "jsonmerge"

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
	// The root genesis is a figaro RoleGenesis message (filtered from
	// rendering/context) — not figwal's generic marker, which would read back
	// as an empty-role message in the IR.
	genesis, _ := json.Marshal(message.Message{Role: message.RoleGenesis})
	return xwal.Config{
		Main:        chanIR,
		Codec:       "jsonl",
		Genesis:     genesis,
		MintTrunkID: hexTrunkID,
		Registry: map[string]xwal.Reducer{
			reducerJSONMerge: {Reduce: chalkboardReduce, Initial: []byte("{}")},
		},
		Channels: []xwal.ChannelSpec{
			{Name: chanIR, Kind: xwal.ChannelLog},
			{Name: chanChalkboard, Kind: xwal.ChannelReducible, Reducer: reducerJSONMerge},
			{Name: chanTurnWAL, Kind: xwal.ChannelLog, SyncMode: xwal.SyncManual, Opaque: true},
		},
	}
}

// XwalStore owns the aria tree (policy over xwal.Trunks).
type XwalStore struct {
	root             string
	cfg              xwal.Config
	mu               sync.Mutex
	trunks           *xwal.Trunks
	topology         atomic.Pointer[topologySnapshot]
	translationReady map[string]bool
	now              func() int64
}

type topologySnapshot struct {
	version         uint64
	nodes           []NodeView
	conversations   []NodeView
	conversationIDs []string
	byID            map[string]NodeView
}

// OpenXwalStore opens the aria tree at root, creating it when absent.
func OpenXwalStore(root string) (*XwalStore, error) {
	if root == "" {
		return nil, fmt.Errorf("xwal store: empty root")
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, err
	}
	s := &XwalStore{
		root: root, cfg: storeConfig(), translationReady: map[string]bool{},
		now: func() int64 { return time.Now().UnixMilli() },
	}
	tr, err := xwal.OpenTrunks(root, s.cfg)
	if err != nil {
		// Not initialized yet: create the genesis root.
		tr, cerr := xwal.CreateTrunks(root, s.cfg)
		if cerr != nil {
			return nil, cerr
		}
		s.trunks = tr
		if err := s.ensureRuntimeChannels(); err != nil {
			_ = tr.Close()
			return nil, err
		}
		return s, nil
	}
	s.trunks = tr
	if err := s.ensureRuntimeChannels(); err != nil {
		_ = tr.Close()
		return nil, err
	}
	return s, nil
}

func (s *XwalStore) ensureRuntimeChannels() error {
	return s.trunks.EnsureChannel(xwal.ChannelSpec{
		Name: chanTurnWAL, Kind: xwal.ChannelLog, SyncMode: xwal.SyncManual, Opaque: true,
	})
}

func (s *XwalStore) ensureChannel(spec xwal.ChannelSpec) error {
	return s.trunks.EnsureChannel(spec)
}

func (s *XwalStore) ensureOpaqueTranslationChannel(legacy, current string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.translationReady[current] {
		return nil
	}

	spec := xwal.ChannelSpec{
		Name: current, Kind: xwal.ChannelLog, SyncMode: xwal.SyncManual, Opaque: true,
	}
	trunks := s.listTrunks()
	present := 0
	for _, trunk := range trunks {
		xw, err := s.trunks.Head(trunk.ID)
		if err != nil {
			return err
		}
		for _, channel := range xw.Channels() {
			if channel.Name == current {
				present++
				if !channel.Opaque {
					_ = xw.Close()
					return fmt.Errorf("translation migration: current channel %q is not opaque", current)
				}
				break
			}
		}
		_ = xw.Close()
	}
	if present != 0 && present != len(trunks) {
		return fmt.Errorf("translation migration: channel %q is installed on only %d of %d trunks", current, present, len(trunks))
	}
	if present == len(trunks) && len(trunks) > 0 {
		for _, trunk := range trunks {
			if err := s.verifyTranslationMigration(trunk.ID, legacy, current); err != nil {
				return err
			}
		}
		s.translationReady[current] = true
		return nil
	}

	snapshots := make(map[string][]xwal.Record, len(trunks))
	for _, trunk := range trunks {
		xw, err := s.trunks.Head(trunk.ID)
		if err != nil {
			return err
		}
		records, err := exactChannelRecords(xw, legacy)
		if err == nil {
			err = validateLegacyTranslation(xw, trunk.ID, legacy, records)
		}
		_ = xw.Close()
		if err != nil {
			return err
		}
		snapshots[trunk.ID] = records
	}
	if err := s.trunks.EnsureChannel(spec); err != nil {
		return err
	}
	for _, trunk := range trunks {
		records := snapshots[trunk.ID]
		if len(records) == 0 {
			continue
		}
		xw, err := s.trunks.Head(trunk.ID)
		if err != nil {
			return err
		}
		for _, record := range records {
			if _, err := xw.Append(current, record.MainLT, record.Payload, record.Meta); err != nil {
				_ = xw.Close()
				return fmt.Errorf("translation migration: append trunk %s main LT %d: %w", trunk.ID, record.MainLT, err)
			}
		}
		if err := xw.SyncChannel(current); err != nil {
			_ = xw.Close()
			return fmt.Errorf("translation migration: sync trunk %s: %w", trunk.ID, err)
		}
		_ = xw.Close()
	}
	s.translationReady[current] = true
	return nil
}

func (s *XwalStore) verifyTranslationMigration(id, legacy, current string) error {
	xw, err := s.trunks.Head(id)
	if err != nil {
		return err
	}
	defer xw.Close()
	oldRecords, err := exactChannelRecords(xw, legacy)
	if err != nil {
		return fmt.Errorf("translation migration: read %s: %w", legacy, err)
	}
	newRecords, err := exactChannelRecords(xw, current)
	if err != nil {
		return fmt.Errorf("translation migration: read %s: %w", current, err)
	}
	if len(newRecords) < len(oldRecords) {
		return fmt.Errorf("translation migration: unsafe incomplete migration for trunk %s: %s has %d records, %s has %d", id, legacy, len(oldRecords), current, len(newRecords))
	}
	for i := range oldRecords {
		if oldRecords[i].MainLT != newRecords[i].MainLT {
			return fmt.Errorf("translation migration: %s and %s differ at record %d main LT", legacy, current, i+1)
		}
		if !bytes.Equal(oldRecords[i].Payload, newRecords[i].Payload) {
			return fmt.Errorf("translation migration: %s and %s differ at record %d payload", legacy, current, i+1)
		}
		if !bytes.Equal(oldRecords[i].Meta, newRecords[i].Meta) {
			return fmt.Errorf("translation migration: %s and %s differ at record %d metadata", legacy, current, i+1)
		}
	}
	return nil
}

func exactChannelRecords(xw *xwal.XWAL, name string) ([]xwal.Record, error) {
	var info *xwal.ChannelInfo
	for _, channel := range xw.Channels() {
		if channel.Name == name {
			channel := channel
			info = &channel
			break
		}
	}
	if info == nil {
		return nil, nil
	}
	first := info.First
	if first == 0 && info.Last > 0 {
		first = 1
	}
	if first == 0 || info.Last < first {
		return nil, nil
	}
	records := make([]xwal.Record, 0, info.Last-first+1)
	seenMain := make(map[uint64]struct{}, info.Last-first+1)
	for lt := first; lt <= info.Last; lt++ {
		indexed, err := xw.ReadAt(name, lt)
		if err != nil {
			return nil, err
		}
		if _, duplicate := seenMain[indexed.MainLT]; duplicate {
			return nil, fmt.Errorf("%s has duplicate main LT %d", name, indexed.MainLT)
		}
		seenMain[indexed.MainLT] = struct{}{}
		record, ok, err := xw.Lookup(name, indexed.MainLT)
		if err != nil {
			return nil, fmt.Errorf("%s exact lookup main LT %d: %w", name, indexed.MainLT, err)
		}
		if !ok {
			return nil, fmt.Errorf("%s exact lookup main LT %d: not found", name, indexed.MainLT)
		}
		record.Payload = append([]byte(nil), record.Payload...)
		record.Meta = append([]byte(nil), record.Meta...)
		records = append(records, record)
	}
	return records, nil
}

func validateLegacyTranslation(xw *xwal.XWAL, id, legacy string, records []xwal.Record) error {
	for _, record := range records {
		if record.MainLT == 0 {
			return fmt.Errorf("translation migration: %s has unscoped root fallback record", legacy)
		}
		if _, ok, err := xw.Lookup(chanIR, record.MainLT); err != nil || !ok {
			return fmt.Errorf("translation migration: %s record at main LT %d is outside trunk %s", legacy, record.MainLT, id)
		}
		var items []json.RawMessage
		if err := json.Unmarshal(record.Payload, &items); err != nil {
			return fmt.Errorf("translation migration: invalid opaque payload at main LT %d: %w", record.MainLT, err)
		}
	}
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
	// The loadout's birth message is renderable (RoleUser, empty content): its
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

// Promote climbs a conversation trunk up `levels` stump-bounded levels,
// relabeling the canonical trunk path (the trunk absorbs its parent trunk's
// run). Returns the number of levels actually climbed. xwal.ErrAtStump means
// the trunk is rooted directly at a loadout — there is nothing to promote into.
func (s *XwalStore) Promote(id string, levels int) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	climbed, err := s.trunks.Promote(id, levels)
	if errors.Is(err, xwal.ErrAtStump) {
		return climbed, ErrAtStump
	}
	return climbed, err
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
	gen, _ := json.Marshal(message.Message{Role: message.RoleUser, Timestamp: s.now()})
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

// NodeView is a read-only snapshot of an aria (trunk) for listing/lineage.
type NodeView struct {
	ID         string
	Parent     string
	Kind       string
	Loadout    string
	Version    string
	Children   []string
	Frozen     bool
	Depth      int
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

// RemoveLeaf deletes a trunk (its subtree) via xwal.Trunks. Trunk-addressed;
// refuses a trunk with live branches unless recursive.
func (s *XwalStore) RemoveLeaf(id string, recursive bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.trunks.Remove(id, recursive)
}
