package store

// XwalStore is figaro's policy layer over xwal.Trunks, which owns the
// fork/trunk mechanics on disk.

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/handle"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/internal/config"
	fwlog "github.com/jack-work/figaro/internal/store/log"
	"github.com/jack-work/figaro/internal/store/segment"
	"github.com/jack-work/figaro/internal/store/xwal"
	"github.com/jack-work/figaro/internal/topo"
)

// trunkScanCount counts figwal trunk-listing scans.
var trunkScanCount atomic.Int64

// Always go through these inside the store so every tree scan is counted.
func (s *XwalStore) listTrunks() []xwal.TrunkInfo {
	trunkScanCount.Add(1)
	// ListLight, not List: List opens every trunk's head (a segment scan) to
	// compute Tip, which figaro never uses.
	return s.trunks.ListLight()
}

func (s *XwalStore) listStumps() []xwal.StumpInfo {
	trunkScanCount.Add(1)
	all := s.trunks.Stumps()
	// Reserved stumps carry machinery, not conversations: never listed, never
	// forked, never bound.
	out := all[:0:0]
	for _, st := range all {
		if isReservedStump(st.Name) {
			continue
		}
		out = append(out, st)
	}
	return out
}

func hexTrunkID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

const mintDraws = 64

var drawTrunkID = hexTrunkID

func mintTrunkID(kind string, taken func(prefix string) bool) string {
	sigil := ""
	if kind == string(kindForm) {
		sigil = formSigil
	}
	id := sigil + drawTrunkID()
	for i := 1; i < mintDraws && taken(id[:len(sigil)+handle.HexLen]); i++ {
		id = sigil + drawTrunkID()
	}
	return id
}

const formSigil = "@"

const (
	chanIR = "ir"
	// chanForm was "chalkboard" through store generation 1; the version gate
	// refuses a generation-1 store rather than reading it as a board with no keys.
	chanForm = "form"
	// chanUI is declared so the schema registry can version it before it carries data.
	chanUI = "ui"

	keyOutfitName = "system.outfit_name"
	keyOutfitVer  = "system.outfit_version"
	// The pre-rename spelling: a stump minted before loadout->outfit states
	// its name under these, and its birth record is canonical, read
	// tolerantly and never rewritten.
	keyLegacyName = "system.loadout_name"
	keyLegacyVer  = "system.loadout_version"

	// rootID: the root is the channel dir itself and carries no trunk id on disk.
	rootID = "null"
)

type nodeKind string

const (
	kindNull         nodeKind = "null"
	kindOutfit       nodeKind = "outfit"
	kindConversation nodeKind = "conversation"
	// kindForm is recorded in the figwal node marker at mint and immutable
	// from then on: binding forks, nothing converts.
	kindForm nodeKind = "form"
)

func formReduce(state, patch []byte) ([]byte, error) {
	snap := form.Snapshot{}
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

var handleIdle atomic.Int64

// SetHandleIdle: call before opening the store; zero leaves figwal's default.
func SetHandleIdle(d time.Duration) { handleIdle.Store(int64(d)) }

// SetSegmentCacheBudget bounds the raw payloads every other cache sits on top
// of: the IR, translation and patch windows cap decoded copies of these bytes.
func SetSegmentCacheBudget(bytes int64) { fwlog.SetPayloadCacheBudget(bytes) }

// SweepSegmentBudget is the payload cache's half of the standing sweep.
func SweepSegmentBudget() (int, int64) { return fwlog.SweepPayloadCache() }

func SegmentCacheBytes() int64  { return fwlog.PayloadCacheBytes() }
func SegmentCacheBudget() int64 { return segment.CacheBudget() }

func SweepSegmentCache(keep int64) (int, int64) { return segment.SweepIdle(keep) }

// SegmentCacheLoads climbing with reads rather than with distinct segments
// means blocks are dropped as fast as they are built.
func SegmentCacheLoads() int64 { return segment.CacheLoads() }

func storeOptions(segmentSize int) xwal.StoreOptions {
	if segmentSize <= 0 {
		var noConfig *config.Loaded // the accessor is nil-safe on purpose
		segmentSize = noConfig.SegmentSize()
	}
	// The root genesis is a RoleGenesis message, not figwal's generic marker,
	// which would read back as an empty-role message in the IR.
	genesis, _ := json.Marshal(message.Message{Role: message.RoleGenesis})
	return xwal.StoreOptions{
		Main: chanIR,
		// figaro owns durability: every append syncs before anything reaches
		// memory, so a background pass could only turn a rejected write
		// durable after the fact.
		NoBackgroundFlush: true,
		IdleUnload:        time.Duration(handleIdle.Load()),
		Codec:             "jsonl",
		SegmentSize:       int64(segmentSize),
		Genesis:           genesis,
		MintTrunkID:       mintTrunkID,
		Reducers: map[string]xwal.Reducer{
			chanForm: {Reduce: formReduce, Initial: []byte("{}")},
		},
		Opaque: []string{
			transChannel("anthropic"),
			transChannel("copilot-messages"),
			transChannel("copilot-responses"),
		},
		// The form is unkeyed: a patch is a declaration of intent, not a fact
		// about a turn.
		Unkeyed: []string{chanForm},
	}
}

type XwalStore struct {
	root     string
	nodeID   string
	mu       sync.Mutex
	trunks   *xwal.Store
	topology atomic.Pointer[topologySnapshot]

	// observed is in-memory; the aria's board is the durable truth.
	observedMu sync.Mutex // writers only
	observed   atomic.Pointer[map[string][]string]
	now        func() int64
	// tree is the presentation hierarchy: what fig ls draws and what a delete
	// takes. Never consulted for forking: that reads .from.
	tree topo.Tree
	// deleting serializes whole deletes: two interleaved would re-home an aria
	// under a parent the other is in the middle of taking.
	deleting sync.Mutex
}

// xwalTopology is the .from adjacency, the only lineage figwal considers authoritative.
type xwalTopology struct{ s *XwalStore }

func (t xwalTopology) From(id string) (string, bool) {
	n, ok := t.s.Node(id)
	if !ok {
		return "", false
	}
	return n.Parent, true
}

func (t xwalTopology) Nodes() []string {
	// Through topologySnapshot, not the raw pointer, so this refreshes as From() does.
	snap := t.s.topologySnapshot()
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

// LoadedHeads: each head is a whole channel's raw payloads, the largest
// resident thing in the daemon. A listing climbs it to the size of the store;
// figwal's idle unload brings it down.
func (s *XwalStore) LoadedHeads() int { return s.trunks.LoadedHeads() }

func (s *XwalStore) Tree() topo.Tree { return s.tree }

func (s *XwalStore) TopologyAdjacency() topo.Topology { return xwalTopology{s} }

// SetTree: without a trunk capability the store keeps the topology tree.
func (s *XwalStore) SetTree(t topo.Tree) { s.tree = t }

type topologySnapshot struct {
	version         uint64
	rev             uint64
	nodes           []NodeView
	conversations   []NodeView
	forms           []NodeView
	conversationIDs []string
	byID            map[string]NodeView

	sortOnce sync.Once
	sorted   []string
}

func (t *topologySnapshot) sortedIDs() []string {
	t.sortOnce.Do(func() {
		ids := make([]string, 0, len(t.conversations)+len(t.forms))
		for _, n := range t.conversations {
			ids = append(ids, n.ID)
		}
		for _, n := range t.forms {
			ids = append(ids, n.ID)
		}
		sort.Strings(ids)
		t.sorted = ids
	})
	return t.sorted
}

func (s *XwalStore) MatchID(prefix string, limit int) []string {
	ids := s.topologySnapshot().sortedIDs()
	var out []string
	for i := sort.SearchStrings(ids, prefix); i < len(ids) && strings.HasPrefix(ids[i], prefix); i++ {
		if len(out) == limit {
			break
		}
		out = append(out, ids[i])
	}
	return out
}

func (t *topologySnapshot) fresh(version, rev uint64) bool {
	return t.version == version && t.rev == rev
}

// presentRev is the presentation clock, zero without a trunk capability.
func (s *XwalStore) presentRev() uint64 {
	if s.tree == nil {
		return 0
	}
	return s.tree.Rev()
}

func OpenXwalStore(root string, segmentSize int) (*XwalStore, error) {
	if root == "" {
		return nil, fmt.Errorf("xwal store: empty root")
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, err
	}
	// Before the store opens: an unmigrated store does not open. The migration
	// takes the store lock itself, so a second process waits rather than
	// half-reading.
	if err := migrateLayout(root); err != nil {
		return nil, err
	}
	// The generation gate speaks before figwal does.
	if err := CheckStoreGeneration(root); err != nil {
		return nil, err
	}
	st, err := xwal.OpenStore(root, storeOptions(segmentSize))
	if err != nil {
		return nil, err
	}
	if err := ensureSchema(root, st); err != nil {
		return nil, err
	}
	nodeID, err := ensureNodeID(root)
	if err != nil {
		st.Close()
		return nil, err
	}
	x := &XwalStore{
		root: root, trunks: st, nodeID: nodeID,
		now: func() int64 { return time.Now().UnixMilli() },
	}
	empty := map[string][]string{}
	x.observed.Store(&empty)
	x.tree = topo.FromTopology(xwalTopology{x})
	return x, nil
}

func (s *XwalStore) Close() error { return s.trunks.Close() }

// migrateLayout brings an older layout up to this build's: every node at
// depth 1, lineage in its own marker. Automatic, not silent, not partial:
// either it finishes or the open fails.
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

// Caller closes.
func (s *XwalStore) openNode(id string) (*xwal.XWAL, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	x, err := s.trunks.Head(id)
	if err == nil {
		return x, nil
	}
	// A stump is not a trunk; it is openable because it holds the birth record
	// that states the outfit's name.
	if sx, serr := s.trunks.StumpHead(id); serr == nil {
		return sx, nil
	}
	return nil, err
}

// outfitStump is the content version alone: the name is already inside the
// hash and the birth record. The "@" makes it unmistakably not an aria id.
func outfitStump(ver string) string { return "@" + ver }

func (s *XwalStore) CreateOutfit(name string, patch message.Patch) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	named := withOutfitName(patch, name)
	ver, err := contentVersion(named)
	if err != nil {
		return "", err
	}
	stump := outfitStump(ver)
	for _, st := range s.trunks.Stumps() {
		if st.Name != stump {
			continue
		}
		// Reuse it only if it minted: a stump whose birth record never landed
		// hands every aria beneath an empty prefix. Existence is not completeness.
		if s.stumpBorn(stump) {
			return stump, nil
		}
		if rerr := s.trunks.RemoveStump(stump); rerr != nil {
			return "", fmt.Errorf("xwal store: half-minted stump %s: %w", stump, rerr)
		}
		break
	}
	if err := s.trunks.CreateStump(stump); err != nil {
		return "", fmt.Errorf("xwal store: create outfit stump: %w", err)
	}
	// The birth message renders the form patch as the outfit's
	// <system-reminder> blocks once in this shared prefix, inherited by every
	// conversation.
	stamped := withOutfitVersion(named, ver)
	if err := s.writeStumpBirth(stump, &stamped); err != nil {
		return "", err
	}
	return stump, nil
}

// ForkWith forks a node and lands a patch on the child in one critical section.
func (s *XwalStore) ForkWith(parent string, atMainLT uint64, patch message.Patch, opts ...BirthOption) (child string, version uint64, err error) {
	return s.forkWithKind(parent, atMainLT, patch, string(kindConversation), births(opts))
}

// CreateForm: parent "" forks the null root, a form parent duplicates its
// state into a fresh @id. No atMainLT: a form's timeline is one ceremonial
// record. A conversation parent is refused; a bound form's fork is the aria's
// fork, through ForkWith.
func (s *XwalStore) CreateForm(parent string, patch message.Patch, opts ...BirthOption) (id string, version uint64, err error) {
	if parent != "" {
		s.mu.Lock()
		kind, known := s.trunks.Kind(parent)
		legacyStump := s.isStumpLocked(parent)
		s.mu.Unlock()
		if !legacyStump && (!known || kind != string(kindForm)) {
			return "", 0, fmt.Errorf("xwal store: create form: parent %s is not an unbound form", parent)
		}
	}
	return s.forkWithKind(parent, 0, patch, string(kindForm), births(opts))
}

// forkWithKind picks the spawn shape from the parent's species, then lands
// the birth patch and its cursor-stamped record in order.
func (s *XwalStore) forkWithKind(parent string, atMainLT uint64, patch message.Patch, kind string, b birthSpec) (child string, version uint64, err error) {
	if patch.IsIdentity() {
		return "", 0, fmt.Errorf("xwal store: fork-with: a fork must carry a patch")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	switch {
	case parent == "":
		child, err = s.trunks.SpawnUnderRootKind(kind)
	case s.isStumpLocked(parent):
		// A stump has no tail to fork: spawning beneath it is what "fork the
		// outfit" means, and the child inherits the birth record every sibling reads.
		child, err = s.trunks.SpawnUnderStumpKind(parent, kind)
	case s.isFormLocked(parent):
		// A live, patchable forking point: a new trunk beneath it. The form is
		// not written or frozen; later patches belong to the form alone.
		child, err = s.trunks.SpawnChildKind(parent, kind)
	case atMainLT == 0:
		child, err = s.trunks.ForkTail(parent)
	default:
		child, err = s.forkAtLocked(parent, atMainLT)
	}
	if err != nil {
		return "", 0, err
	}
	version, err = s.writeBirth(child, patch, b)
	if err != nil {
		return "", 0, err
	}
	return child, version, nil
}

// Caller holds s.mu.
func (s *XwalStore) isFormLocked(id string) bool {
	kind, ok := s.trunks.Kind(id)
	return ok && kind == string(kindForm)
}

// writeBirth appends a node's birth patch and the renderable record that carries
// its cursor stamp. Caller holds s.mu.
func (s *XwalStore) writeBirth(node string, patch message.Patch, b birthSpec) (uint64, error) {
	x, err := s.trunks.Head(node)
	if err != nil {
		return 0, err
	}
	defer x.Close()
	pb, err := json.Marshal(patch)
	if err != nil {
		return 0, err
	}
	next := mainTailOf(x) + 1
	version, err := x.Append(chanForm, next, pb, nil)
	if err != nil {
		return 0, err
	}
	gen, _ := json.Marshal(message.Message{Role: message.RoleInput, Timestamp: s.now(), Principal: b.mintedBy})
	glt, err := x.AppendMain(gen, nil)
	if err != nil {
		return 0, err
	}
	if glt != next {
		return 0, fmt.Errorf("xwal store: %s birth record landed at %d, form patch keyed to %d",
			node, glt, next)
	}
	// Durable before anything spawns beneath it: a crash between here and the
	// next flush would orphan a child's fork base.
	return version, x.SyncCoherent()
}

// isStumpLocked reports whether an id names a stump. Caller holds s.mu.
func (s *XwalStore) isStumpLocked(id string) bool {
	for _, st := range s.trunks.Stumps() {
		if st.Name == id {
			return true
		}
	}
	return false
}

// stumpBorn reports whether a stump has its birth record. Caller holds s.mu.
func (s *XwalStore) stumpBorn(stump string) bool {
	x, err := s.trunks.StumpHead(stump)
	if err != nil {
		return false
	}
	defer x.Close()
	return mainTailOf(x) > 0
}

func (s *XwalStore) CreateConversation(outfitID string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id, err := s.trunks.SpawnUnderStump(outfitID)
	if err != nil {
		return "", fmt.Errorf("xwal store: spawn conversation: %w", err)
	}
	// No birth message: the conversation inherits the outfit's rendered prefix
	// via the fork watermark; its own IR starts empty (first turn appends).
	return id, nil
}

// Fork: the aria id is stable (cont == id); only the alternative is new.
func (s *XwalStore) Fork(id string) (cont, alt string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	alt, err = s.trunks.ForkTail(id)
	if err != nil {
		return "", "", err
	}
	return id, alt, nil
}

// ForkAt shares [1..atMainLT] and mints an empty alternative diverging at
// atMainLT+1; cont == id. At or past the tail it degenerates to a tail fork.
func (s *XwalStore) ForkAt(id string, atMainLT uint64) (cont, alt string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	alt, err = s.forkAtLocked(id, atMainLT)
	if err != nil {
		return "", "", err
	}
	return id, alt, nil
}

// Caller holds s.mu.
func (s *XwalStore) forkAtLocked(id string, atMainLT uint64) (string, error) {
	var alt string
	var err error
	owner, oerr := s.trunks.Owner(id, atMainLT)
	if oerr != nil {
		return "", oerr
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
		return "", err
	}
	return alt, nil
}

// Promote edits the presentation hierarchy only, O(1) in history length.
// ErrAtStump: nothing above to promote into.
func (s *XwalStore) Promote(id string, levels int) (int, error) {
	// No s.mu here: the tree resolves lineage through Node, which refreshes
	// the topology snapshot under s.mu. The tree carries its own lock.
	if s.tree == nil {
		return 0, ErrNoTrunkCapability
	}
	for climbed := 0; climbed < levels; climbed++ {
		if !s.promotableInto(id) {
			if climbed > 0 {
				return climbed, nil
			}
			return 0, ErrAtStump
		}
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

func (s *XwalStore) promotableInto(id string) bool {
	parent, ok := s.tree.Parent(id)
	if !ok || parent == "" {
		return false
	}
	node, ok := s.Node(parent)
	return ok && node.Kind == string(kindConversation)
}

func (s *XwalStore) OwnerOf(id string, atMainLT uint64) (xwal.Owner, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.trunks.Owner(id, atMainLT)
}

// Caller holds s.mu.
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

	// The board patch goes first, and the order is the whole point.
	next := mainTailOf(x) + 1
	if _, err := x.Append(chanForm, next, pb, nil); err != nil {
		return err
	}
	gen, _ := json.Marshal(message.Message{Role: message.RoleInput, Timestamp: s.now()})
	glt, err := x.AppendMain(gen, nil)
	if err != nil {
		return err
	}
	if glt != next {
		// Nothing else may write to a stump; if this fires, the patch is keyed
		// to a record that does not exist.
		return fmt.Errorf("xwal store: stump %s birth record landed at %d, board patch keyed to %d",
			stump, glt, next)
	}
	// Durable before conversations spawn under the stump: a crash between
	// spawn and the next flush would orphan the children's fork base.
	return x.SyncCoherent()
}

// mainTailOf keys a record to the LT the next append will take.
func mainTailOf(x *xwal.XWAL) uint64 {
	for _, ch := range x.Channels() {
		if ch.Name == chanIR {
			return ch.Last
		}
	}
	return 0
}

// OutfitVersion is an outfit's IDENTITY: the value-stable hash of the birth
// record it writes, minus the version field, which cannot cover its own hash.
func OutfitVersion(name string, patch message.Patch) (string, error) {
	return contentVersion(withOutfitName(patch, name))
}

// LegacyOutfitVersion is the pre-name hash; delete it when no store in use
// still holds a stump from an older build.
func LegacyOutfitVersion(patch message.Patch) (string, error) {
	return contentVersion(patch)
}

// ContentVersion is the value-stable content hash of a patch.
func ContentVersion(patch message.Patch) (string, error) { return contentVersion(patch) }

func contentVersion(patch message.Patch) (string, error) {
	body, err := json.Marshal(patch)
	if err != nil {
		return "", err
	}
	return segment.ValueHash(body)
}

func withOutfitName(p message.Patch, name string) message.Patch {
	return withKey(p, keyOutfitName, name)
}

func withOutfitVersion(p message.Patch, ver string) message.Patch {
	return withKey(p, keyOutfitVer, ver)
}

func withKey(p message.Patch, key, value string) message.Patch {
	b, _ := json.Marshal(value)
	return form.Merge(p, form.Build(form.Snapshot{},
		map[string]json.RawMessage{key: b}, nil))
}

type NodeView struct {
	ID     string
	Parent string
	Kind   string
	// Stump is where the node's data came from, by the topology walk; a
	// promote moves where a row appears, never this.
	Stump      string
	Outfit     string // the stump's label; filled by the backend
	Version    string // the stump's content version; same
	Trunk      string
	Vector     []int
	BranchedLT uint64 // main-LT this trunk diverged from its parent
	// Present is where the row APPEARS: the topology edge unless a promote
	// moved it. Vector follows Present, so a listing draws one tree.
	Present string
}

// view: a top-level conversation's parent is its outfit stump, or the root
// when outfitless.
func (s *XwalStore) view(t xwal.TrunkInfo, at map[string]place) NodeView {
	parent := t.Parent
	if parent == "" {
		if t.Stump != "" {
			parent = t.Stump // top-level conversation: nests under its outfit
		} else {
			parent = rootID // outfitless top-level conversation
		}
	}
	p := at[t.ID]
	// The trunk's own kind from its figwal marker; empty (never written) means
	// conversation.
	kind := t.Kind
	if kind == "" {
		kind = string(kindConversation)
	}
	return NodeView{
		ID: t.ID, Parent: parent, Kind: kind, Trunk: t.ID,
		Stump: p.stump, Vector: p.vec, BranchedLT: t.BranchedLT,
	}
}

// place is a conversation trunk's fork-tree vector (child-index path,
// siblings ordered by id) and the stump it was born under; one map because
// both are found by the same walk.
type place struct {
	vec   []int
	stump string
}

func (s *XwalStore) vectorsLocked(infos []xwal.TrunkInfo) map[string]place {
	live := make(map[string]bool, len(infos))
	for _, ti := range infos {
		// Forms are not part of the conversation fork-tree: a form-born aria is
		// a top-level conversation, not a branch of an invisible parent.
		if ti.Kind == string(kindForm) {
			continue
		}
		live[ti.ID] = true
	}
	type seed struct{ id, stump string }
	kids := map[string][]string{}
	var roots []seed
	for _, ti := range infos {
		if ti.Kind == string(kindForm) {
			continue
		}
		if ti.Parent != "" && live[ti.Parent] {
			kids[ti.Parent] = append(kids[ti.Parent], ti.ID) // branch of a conversation
		} else {
			roots = append(roots, seed{ti.ID, ti.Stump}) // top-level (parent is a stump/root/form)
		}
	}
	sort.Slice(roots, func(i, j int) bool { return roots[i].id < roots[j].id })
	for k := range kids {
		sort.Strings(kids[k])
	}
	// The stump rides alongside the vector: figwal names it only for a trunk
	// rooted directly at one, so a branch inherits it.
	at := make(map[string]place, len(infos))
	var assign func(id string, prefix []int, from string)
	assign = func(id string, prefix []int, from string) {
		at[id] = place{prefix, from}
		for i, c := range kids[id] {
			assign(c, append(append([]int(nil), prefix...), i), from)
		}
	}
	for i, r := range roots {
		assign(r.id, []int{i}, r.stump)
	}
	return at
}

// presentLocked stamps every node's display parent and the vectors that
// follow from it, returning the presentation revision it read.
func (s *XwalStore) presentLocked(nodes []NodeView) uint64 {
	var edges map[string]string
	var rev uint64
	if s.tree != nil {
		edges, rev = s.tree.Edges(), s.tree.Rev()
	}
	parent := make(map[string]string, len(nodes))
	for _, n := range nodes {
		parent[n.ID] = n.Parent
	}
	pres := topo.Present(parent, edges)
	for i := range nodes {
		nodes[i].Present = pres[nodes[i].ID]
	}
	if len(edges) == 0 {
		return rev
	}
	vecs := presentVectors(nodes, pres)
	for i := range nodes {
		if v, ok := vecs[nodes[i].ID]; ok {
			nodes[i].Vector = v
		}
	}
	return rev
}

// presentVectors is vectorsLocked over the display edges.
func presentVectors(nodes []NodeView, pres map[string]string) map[string][]int {
	live := make(map[string]bool, len(nodes))
	for _, n := range nodes {
		if n.Kind == string(kindConversation) {
			live[n.ID] = true
		}
	}
	kids := map[string][]string{}
	var roots []string
	for _, n := range nodes {
		if !live[n.ID] {
			continue
		}
		if up := pres[n.ID]; up != "" && live[up] {
			kids[up] = append(kids[up], n.ID)
		} else {
			roots = append(roots, n.ID)
		}
	}
	sort.Strings(roots)
	for k := range kids {
		sort.Strings(kids[k])
	}
	out := make(map[string][]int, len(live))
	var assign func(id string, prefix []int)
	assign = func(id string, prefix []int) {
		if _, seen := out[id]; seen {
			return
		}
		out[id] = prefix
		for i, c := range kids[id] {
			assign(c, append(append([]int(nil), prefix...), i))
		}
	}
	for i, r := range roots {
		assign(r, []int{i})
	}
	return out
}

func (s *XwalStore) topologySnapshot() *topologySnapshot {
	// Keyed on both clocks: figwal's topology version and the presentation
	// revision. A promote moves no bytes, so the first one does not move.
	version, rev := s.trunks.Version(), s.presentRev()
	if snapshot := s.topology.Load(); snapshot != nil && snapshot.fresh(version, rev) {
		return snapshot
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	version, rev = s.trunks.Version(), s.presentRev()
	if snapshot := s.topology.Load(); snapshot != nil && snapshot.fresh(version, rev) {
		return snapshot
	}

	infos := s.listTrunks()
	at := s.vectorsLocked(infos)
	nodes := make([]NodeView, 0, len(infos)+1)
	for _, t := range infos {
		nodes = append(nodes, s.view(t, at))
	}
	nodes = append(nodes, NodeView{ID: rootID, Kind: string(kindNull), Trunk: rootID})
	for _, st := range s.listStumps() {
		nodes = append(nodes, NodeView{ID: st.Name, Kind: string(kindOutfit), Parent: rootID, Stump: st.Name})
	}
	rev = s.presentLocked(nodes)

	conversations := make([]NodeView, 0, len(nodes))
	var forms []NodeView
	ids := make([]string, 0, len(nodes))
	byID := make(map[string]NodeView, len(nodes))
	for _, node := range nodes {
		// Split by species: `fig ls` lists conversations, and a form would be
		// an aria-shaped row for a thing with no turns.
		switch node.Kind {
		case string(kindForm):
			forms = append(forms, node)
		case string(kindConversation):
			conversations = append(conversations, node)
			ids = append(ids, node.ID)
		}
		byID[node.ID] = node
	}
	snapshot := &topologySnapshot{
		version:         version,
		rev:             rev,
		nodes:           nodes,
		conversations:   conversations,
		forms:           forms,
		conversationIDs: ids,
		byID:            byID,
	}
	s.topology.Store(snapshot)
	return snapshot
}

func (s *XwalStore) Conversations() []NodeView {
	return append([]NodeView(nil), s.topologySnapshot().conversations...)
}

func (s *XwalStore) Forms() []NodeView {
	return append([]NodeView(nil), s.topologySnapshot().forms...)
}

func (s *XwalStore) ConversationIDs() []string {
	return append([]string(nil), s.topologySnapshot().conversationIDs...)
}

// Nodes is every trunk plus the root and every outfit stump.
func (s *XwalStore) Nodes() []NodeView {
	return append([]NodeView(nil), s.topologySnapshot().nodes...)
}

func (s *XwalStore) Node(id string) (NodeView, bool) {
	node, ok := s.topologySnapshot().byID[id]
	return node, ok
}

func (s *XwalStore) RemoveLeaf(id string, recursive bool, bury func([]string)) error {
	s.deleting.Lock()
	defer s.deleting.Unlock()
	// Refuse before touching anything: the boundary repair rewrites surviving arias.
	taken := s.tree.DeleteSet(id)
	if !recursive && len(taken) > 1 {
		return fmt.Errorf("%w: %q has %d; -r takes them too", ErrHasBranches, id, len(taken)-1)
	}
	if bury != nil {
		bury(taken)
	}
	// Repair the boundary first: every survivor reading its history through
	// the delete set absorbs that prefix. Only then unlink, so a crash between
	// the two leaves survivors reading through directories still present.
	homes := map[string]string{}
	// Anything drawn under something this delete takes is re-drawn somewhere
	// that survives; falling back to the topology would put it under the
	// genesis root with no outfit.
	doomed := make(map[string]bool, len(taken))
	for _, t := range taken {
		doomed[t] = true
	}
	for child, up := range s.tree.Edges() {
		if doomed[child] || !doomed[up] {
			continue
		}
		homes[child] = s.survivingHome(child, taken)
	}
	for _, orphan := range s.deleteOrphans(id) {
		// Where it is drawn today, remembered before the detach empties its .from.
		homes[orphan] = s.survivingHome(orphan, taken)
		// Boundary speaks in aria ids; Detach addresses the node directory.
		node, ok := s.trunks.HeadNode(orphan)
		if !ok {
			return fmt.Errorf("%w: no node for %s", ErrWouldOrphan, orphan)
		}
		if err := s.trunks.Detach(node); err != nil {
			return fmt.Errorf("%w: detaching %s: %v", ErrWouldOrphan, orphan, err)
		}
	}
	// The hosting stump is read before the removal.
	stump := s.topologySnapshot().byID[id].Stump
	if err := s.removeLocked(id, recursive); err != nil {
		return err
	}
	// Presentation is repaired with s.mu released: an edge resolves through
	// the topology snapshot, which takes that lock itself.
	if err := s.tree.Forget(taken...); err != nil {
		slog.Warn("forget presentation edges", "aria", id, "err", err)
	}
	keep := false
	for orphan, home := range homes {
		if home == "" {
			continue
		}
		if home == stump {
			keep = true
		}
		if err := s.tree.Reparent(orphan, home); err != nil {
			slog.Warn("rehome detached aria", "aria", orphan, "under", home, "err", err)
		}
	}
	// The stump goes only if nothing is left drawn beneath it.
	if !keep {
		s.collectStumpAfterDelete(stump)
	}
	return nil
}

// removeLocked is the unlink; stump collection is deferred because it depends
// on where the survivors end up.
func (s *XwalStore) removeLocked(id string, recursive bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.trunks.Remove(id, recursive)
}

func (s *XwalStore) collectStumpAfterDelete(stump string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.collectStump(stump)
}

// survivingHome is the nearest place above id that outlives this delete, else
// the stump it was born under.
func (s *XwalStore) survivingHome(id string, taken []string) string {
	doomed := make(map[string]bool, len(taken))
	for _, t := range taken {
		doomed[t] = true
	}
	seen := map[string]bool{id: true}
	for up, ok := s.tree.Parent(id); ok && up != ""; up, ok = s.tree.Parent(up) {
		if seen[up] {
			break
		}
		seen[up] = true
		if !doomed[up] {
			return up
		}
	}
	if node, ok := s.Node(id); ok && node.Stump != "" && !doomed[node.Stump] {
		return node.Stump
	}
	return ""
}

// CollectStump refuses one still hosting arias; the caller is expected to
// have checked, so a refusal is a race.
func (s *XwalStore) CollectStump(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.trunks.RemoveStump(id)
}

func (s *XwalStore) collectStump(name string) {
	if name == "" {
		return
	}
	for _, st := range s.trunks.Stumps() {
		if st.Name != name {
			continue
		}
		if len(st.Children) > 0 {
			return // still hosting arias; nothing to collect
		}
		if err := s.trunks.RemoveStump(name); err != nil {
			slog.Warn("collect outfit stump", "stump", name, "err", err)
		}
		return
	}
}

// Normalize detaches every aria presented under something it was not born
// under: each absorbs the prefix it reads through an ancestor, so a delete's
// boundary is empty afterwards whatever the presentation hierarchy says.
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

// LastTS never wakes an agent: it opens a store handle, not a figaro. Zero
// for pre-timestamp history.
func (s *XwalStore) LastTS(id string) int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Trunk first, by map lookup: a stump scan per row made a listing O(n²).
	if _, ok := s.trunks.Kind(id); ok {
		return s.trunks.LastTS(id)
	}
	return s.trunks.StumpLastTS(id)
}

const KindForm = string(kindForm)

// librettoCursorPrefix namespaces observed-form positions in the main
// record's cursor map; the value is the libretto's version, never the source's.
const librettoCursorPrefix = "libretto:"

func studyCursors(cursors map[string]uint64) map[string]uint64 {
	var out map[string]uint64
	for k, v := range cursors {
		if rest, ok := strings.CutPrefix(k, librettoCursorPrefix); ok {
			if out == nil {
				out = map[string]uint64{}
			}
			out[rest] = v
		}
	}
	return out
}

// SetObservedForms: the list mirrors the agent's board; the store holds it in
// memory only, since the board is the durable truth and the agent re-declares
// on boot.
func (s *XwalStore) SetObservedForms(ariaID string, formIDs []string) {
	s.observedMu.Lock()
	cur := *s.observed.Load()
	next := make(map[string][]string, len(cur)+1)
	for k, v := range cur {
		next[k] = v
	}
	if len(formIDs) == 0 {
		delete(next, ariaID)
	} else {
		next[ariaID] = append([]string(nil), formIDs...)
	}
	s.observed.Store(&next)
	s.observedMu.Unlock()
}

// observedCursors reads the libretto's version, never the source's: the
// libretto is the copy the translator renders from, and it outlives its source.
func (s *XwalStore) observedCursors(ariaID string) map[string]uint64 {
	ids := (*s.observed.Load())[ariaID]
	if len(ids) == 0 {
		return nil
	}
	out := make(map[string]uint64, len(ids))
	for _, fid := range ids {
		if v, ok := s.formTail(LibrettoID(fid)); ok {
			out[librettoCursorPrefix+fid] = v
		}
	}
	return out
}

// formTail reads the hot handle without a Form replay.
func (s *XwalStore) formTail(id string) (uint64, bool) {
	x, err := s.openNode(id)
	if err != nil {
		return 0, false
	}
	defer x.Close()
	for _, c := range x.Channels() {
		if c.Name == chanForm {
			return c.Last, true
		}
	}
	return 0, false
}

// HandleIdleForTest reads the variable the store consults, so a config test
// can tell a wired knob from an unwired one.
func HandleIdleForTest() time.Duration { return time.Duration(handleIdle.Load()) }
