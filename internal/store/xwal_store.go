package store

// XwalStore is figaro's aria tree, a thin policy layer over figwal's
// xwal.Trunks (which owns the fork/trunk mechanics on disk). figaro keeps
// only policy:
//
//	root (null) ──CreateStump──> outfit (stump) ──SpawnUnderStump──> conversation
//	                                                ──ForkTail/interior fork──> branch…
//
//   - root: the channel dir itself (xwal.CreateTrunks genesis). Markerless,
//     ceremonial: the "null" anchor. Addressed by the rootID sentinel.
//   - outfit: a markerless stump (CreateStump) holding a renderable RoleInput
//     birth message that carries the outfit's form stamp
//     (system.outfit_name/version). One per (name, content-version), and its
//     id IS that version, so the dedup map lives on disk (Stumps()): no
//     policy side-file. Ceremonial.
//   - conversation: SpawnUnderStump(outfit): inherits the outfit's
//     rendered prefix via the fork watermark. A live trunk.
//
// The aria id IS the trunk id (stable across forks: the continuation keeps
// it). Trunk identity, the node tree, and fork mechanics live on disk in
// figwal; figaro derives outfits/null from the stump/root structure.
//
// WHERE THIS IS GOING (cast objects). A stump is very nearly a cast object
// already: a durable reducible thing that an aria observes. The difference is
// one rule, a stump cannot be PATCHED, only forked, and that rule is what
// makes everything above true. Minting an aria will become "fork a cast
// object; the fork backs the figaro, the object keeps its own history", which
// is exactly SpawnUnderStump with the parent allowed to go on living.
//
// So the vocabulary settles as: an OUTFIT is a named spec; a spec materializes
// as a cast object; a cast object is a stump when it is closed to patches, and
// an ordinary object when it is not; a stump can be used as a spec in turn,
// and can be forked for a regular object as well as for an aria. Something may
// eventually re-materialize stumps from specs when the files change.
//
// Stump VERSIONING is a different axis from an object's history, and stays
// that way: a new version is a whole new stump with a different hash, because
// no version can ever be produced FROM a stump. That is why the id here is a
// content hash and nothing else, and why nothing renames one.

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

	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/form"
	"github.com/jack-work/figaro/internal/message"
	"github.com/jack-work/figaro/internal/topo"
	fwlog "github.com/jack-work/figaro/internal/store/log"
	"github.com/jack-work/figaro/internal/store/segment"
	"github.com/jack-work/figaro/internal/store/xwal"
)

// trunkScanCount counts calls into figwal's trunk-listing accessors
// (Trunks.ListLight + Trunks.Stumps). It is the proxy the benchmark asserts
// on to catch a fan-out regression (a listing that rescans the tree N times
// instead of once). ListLight itself no longer opens trunk heads.
var trunkScanCount atomic.Int64

// listTrunks / listStumps wrap the figwal accessors so every tree scan is
// counted. Always go through these inside the store.
func (s *XwalStore) listTrunks() []xwal.TrunkInfo {
	trunkScanCount.Add(1)
	// ListLight, not List: figaro never uses TrunkInfo.Tip, and List opens
	// every trunk's head (a segment scan) just to compute it. ListLight is
	// all in-memory + a cheap .fork read: the difference is `fig ls` at
	// ~300ms vs ~tens of ms on a store with many/large arias.
	return s.trunks.ListLight()
}

func (s *XwalStore) listStumps() []xwal.StumpInfo {
	trunkScanCount.Add(1)
	all := s.trunks.Stumps()
	// Reserved stumps carry MACHINERY, not conversations, and none of them
	// belong in a listing: the topology form describes the very tree this
	// list builds (left in, the hierarchy grows a row about itself), and a
	// libretto is a derived copy of a form the user already sees. Both are
	// "never listed, never forked, never bound" (durable-forms §11, §12.2).
	//
	// Found live rather than in a test: `fig ls -g` drew a @libretto:: row
	// the moment the study verb minted one.
	out := all[:0:0]
	for _, st := range all {
		if isReservedStump(st.Name) {
			continue
		}
		out = append(out, st)
	}
	return out
}

// hexTrunkID mints an opaque aria/trunk id (the same 4-byte hex form figaro
// has always used for aria handles), so conversation ids read like real
// handles rather than sequential "t<N>".
func hexTrunkID() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// mintTrunkID gives each species its id shape: unbound forms mint as
// "@<hex>": the sigil that makes a form id unmistakably not an aria id
// (the same convention retired stump ids established, so a legacy stump
// id already reads as what it now is: a form). Everything else mints
// bare hex, exactly as arias always have.
func mintTrunkID(kind string) string {
	if kind == string(kindForm) {
		return formSigil + hexTrunkID()
	}
	return hexTrunkID()
}

// formSigil prefixes every unbound form id.
const formSigil = "@"

const (
	chanIR = "ir"
	// chanForm is the form channel: the aria's state. It was "form" on
	// disk through store generation 1; generation 2 renamed the directory with
	// the concept, and the version gate refuses a generation-1 store rather
	// than reading it as a board with no keys.
	chanForm = "form"
	// chanUI is the derived turn-shaped UI IR cache (Phase 4). Declared here
	// so the schema registry can version it before it carries data.
	chanUI = "ui"

	keyOutfitName = "system.outfit_name"
	keyOutfitVer  = "system.outfit_version"
	// The pre-rename spelling. A stump minted before the loadout->outfit rename
	// states its name under these, and its birth record is canonical: it is
	// read tolerantly, never rewritten. Without this every aria minted before
	// that rename listed with a blank OUTFIT column: 463 of them in this
	// author's store, silently, since the rename shipped.
	keyLegacyName = "system.loadout_name"
	keyLegacyVer  = "system.loadout_version"

	// rootID is the ceremonial "null" anchor's display id. The root is the
	// channel dir itself: it carries no trunk id on disk: so figaro names
	// it with a stable sentinel for listing/lineage.
	rootID = "null"
)

type nodeKind string

const (
	kindNull         nodeKind = "null"
	kindOutfit       nodeKind = "outfit"
	kindConversation nodeKind = "conversation"
	// kindForm is an unbound form: a live, patchable forking point.
	// Recorded in the figwal node marker at mint, immutable from then on -
	// binding forks, nothing converts.
	kindForm nodeKind = "form"
)

// formReduce folds a message.Patch (JSON) onto a form
// snapshot (JSON state): figaro's reducer for the form channel.
//
// The Snapshot's MarshalJSON/UnmarshalJSON are called DIRECTLY rather than
// through json.Marshal/json.Unmarshal, and that is not a style tic: for a
// type with custom JSON hooks, encoding/json pre-scans the input before
// handing it to an Unmarshaler and re-scans a Marshaler's output before
// emitting it. On a 15KB board each of those doubles the cost (measured:
// 97µs -> 188µs decode, 76µs -> 152µs encode), and this reducer runs once
// per WAL record on segment rollover and fork. The bytes are identical
// either way: TestSnapshotDirectCodecMatchesEncodingJSON pins that.
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

// segmentSize bounds one WAL segment file. figwal's 64MB default is a
// server-log figure: MEASURED on the author's store (300 segments, 18262 IR
// entries, 29MB: 1.6KB/entry; biggest single segment 1.78MB/1624 entries),
// nothing has ever rolled and nothing ever would, which makes
// SegmentBaseIndexes: the coarse "which file holds LT N" index a lazy read
// wants, a constant function.
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
// Affects new segments only: existing arias keep their oversized files and
// simply stop growing them.
// handleIdle is figwal's IdleUnload: how long a lineage's in-RAM head
// survives without an append or a read. The second of the three idle clocks
// (agent eviction, this, the writer's linger), and the only one that was
// never wired to config. Package level and set before the store opens, like
// the others.
var handleIdle atomic.Int64

// SetHandleIdle sets figwal's head-unload window. Call before opening the
// store; zero leaves figwal's own default.
func SetHandleIdle(d time.Duration) { handleIdle.Store(int64(d)) }

// SetSegmentCacheBudget bounds the raw segment payloads figwal holds across
// every open channel. This is the bound every other cache in figaro sits on
// top of: the IR window, the translation window and the patch window all cap
// DECODED copies of these bytes.
func SetSegmentCacheBudget(bytes int64) { fwlog.SetPayloadCacheBudget(bytes) }

// SegmentCacheBytes reports what figwal currently holds against that budget,
// and SegmentCacheBudget the budget itself.
func SegmentCacheBytes() int64  { return fwlog.PayloadCacheBytes() }
func SegmentCacheBudget() int64 { return segment.CacheBudget() }

// SweepSegmentCache drops payload blocks unread for `keep` sweeps; see
// segment.SweepIdle.
func SweepSegmentCache(keep int64) (int, int64) { return segment.SweepIdle(keep) }

// SegmentCacheLoads counts whole-segment loads. Climbing with READS rather
// than with distinct segments means blocks are being dropped as fast as they
// are built, and every read is paying for a segment.
func SegmentCacheLoads() int64 { return segment.CacheLoads() }

func storeOptions(segmentSize int) xwal.StoreOptions {
	if segmentSize <= 0 {
		var noConfig *config.Loaded // the accessor is nil-safe on purpose
		segmentSize = noConfig.SegmentSize()
	}
	// The root genesis is a figaro RoleGenesis message (filtered from
	// rendering/context): not figwal's generic marker, which would read back
	// as an empty-role message in the IR.
	genesis, _ := json.Marshal(message.Message{Role: message.RoleGenesis})
	return xwal.StoreOptions{
		Main: chanIR,
		// figaro owns its own durability: every append syncs before anything
		// reaches memory, so a later background pass has no opinion left to
		// offer and could only turn a rejected write durable after the fact.
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
		// The form is UNKEYED: a patch is a declaration of intent, not
		// a fact about a turn, so it should not have to read the timeline to
		// be written. That is what lets a `set` land mid-turn.
		//
		// The translations channels stay KEYED, and deliberately: their main
		// LT is a lookup key ("the provider message for turn k"), and a
		// translation is derived AFTER its turn exists, so there is no
		// moment at which the main record could stamp a cursor for it.
		Unkeyed: []string{chanForm},
	}
}

// XwalStore owns the aria tree (policy over xwal.Store, whose flusher
// owns all durability: every append syncs before it is visible, and the
// background flush is off).
type XwalStore struct {
	// keepStump is the stump collection spares (the live default). One
	// string, published atomically: it used to carry a lock of its own
	// precisely because collectStump runs under s.mu and KeepStump does not,
	// so sharing s.mu would invert the order. A pointer swap has no order to
	// invert.
	keepStump atomic.Pointer[string]

	root     string
	mu       sync.Mutex
	trunks   *xwal.Store
	topology atomic.Pointer[topologySnapshot]

	// observed: ariaID → the form ids its IR appends stamp (study
	// subscriptions). In-memory; the aria's board is the durable truth.
	observedMu sync.Mutex
	observed   map[string][]string
	now        func() int64
	// tree is the PRESENTATION hierarchy: what fig ls draws and what a
	// delete takes. Never consulted for forking: that reads .from.
	tree topo.Tree
	// deleting serializes whole deletes. A delete reads where the
	// survivors are drawn, unlinks, and writes them somewhere that
	// outlived it; two of those interleaved re-home an aria under a
	// parent the other one is in the middle of taking.
	deleting sync.Mutex
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
	// THROUGH topologySnapshot, not the raw pointer. From() resolves via
	// s.Node, which refreshes; reading s.topology directly here answered
	// from whatever was last built, so a delete computed its set from a
	// topology that predated the fork it was meant to include. The check is
	// two loads and a compare when nothing has moved.
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

// LoadedHeads is how many lineage heads figwal holds in memory. Each one is
// a whole channel's raw payloads (log.buildOwnSnapshot copies every record),
// so this is the largest resident thing in the daemon and nothing reported
// it. A listing touches every node for recency, so it climbs to the size of
// the store and comes back down on figwal's idle unload.
func (s *XwalStore) LoadedHeads() int { return s.trunks.LoadedHeads() }

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
	rev             uint64
	nodes           []NodeView
	conversations   []NodeView
	forms           []NodeView
	conversationIDs []string
	byID            map[string]NodeView
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
	// all -- and before THAT was true, it opened reporting its outfits and
	// none of its arias. The single writer (the daemon) owns this; the
	// migration takes the store lock itself, so a second process waits for
	// a store rather than half-reading one.
	if err := migrateLayout(root); err != nil {
		return nil, err
	}
	// The generation gate speaks BEFORE figwal does. A store from an older
	// generation names its channels differently, and figwal would refuse it
	// first with a message about a missing reducer.
	if err := CheckStoreGeneration(root); err != nil {
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
		observed: map[string][]string{},
		now:      func() int64 { return time.Now().UnixMilli() },
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

// openNode opens the xwal for an aria id (the trunk's live head). Caller
// closes it.
//
// UNEXPORTED, and that is the whole point of it. It hands back a raw
// *xwal.XWAL, whose Append takes (channel string, key uint64, payload
// []byte) -- so a caller holding one can put HAND-BUILT BYTES on the channel
// named "form" in a single line: past json.Marshal, past the typed
// message.Patch every legitimate writer goes through, and past Trunks' poison
// and dirty bookkeeping. Every other route to an aria's board is typed
// (Backend.ApplyForm* and *Form's methods all take message.Patch), so this
// was the one door in the write side that took bytes from outside the
// package.
//
// It was closed by VISIBILITY rather than by a test, because a test that
// asserts "nobody outside package store calls this" is a rule that rots,
// while an unexported identifier is the compiler saying it permanently. The
// campaign's own standard -- a hazard test must be proven to reach by failing
// to compile -- is satisfied here directly: the unexport IS the failure to
// compile.
//
// What it does NOT close: figwal's own exported API. XwalStore.trunks is an
// *xwal.Store, and any package in this module may import
// github.com/jack-work/figwal/xwal and open the same directories itself. That
// door belongs to the dependency and no visibility change here reaches it;
// what holds it shut is import discipline, which is a rule and not a shape.
// Only internal/store imports figwal today, with one read-only exception:
// internal/cli/angelus_client.go calls xwal.NeedsFlatten to decide whether a
// layout needs flattening. It survives untouched -- it reads, it never
// appends, and it never names a channel.
func (s *XwalStore) openNode(id string) (*xwal.XWAL, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	x, err := s.trunks.Head(id)
	if err == nil {
		return x, nil
	}
	// A stump is not a trunk, and its branch has its own accessor. It is
	// openable for the same reason it is worth opening: it holds the birth
	// record that states the outfit's name.
	if sx, serr := s.trunks.StumpHead(id); serr == nil {
		return sx, nil
	}
	return nil, err
}

// outfitStump is a stump's id: its content version, and nothing else. The
// name is already inside the hash (see OutfitVersion) and inside the birth
// record the stump writes, so putting it in the id too would be a compound key
// for something with one key. The "@" makes it unmistakably not an aria id.
//
// Stumps minted before this carry "<name>@<version>". They are not renamed and
// need no migration: nothing parses a stump id, and their label is read from
// their own record like every other stump's.
func outfitStump(ver string) string { return "@" + ver }

// CreateOutfit returns the outfit id for this outfit, materializing it as a
// markerless stump under the root if it does not exist yet.
//
// The id is `<name>@<version>`, and the VERSION is the identity: the name is
// folded into the hashed content (see OutfitVersion), so the name in the id is
// a readable restatement of something the hash already covers, not a second
// key. Two spellings that produce the same name and the same patch are the
// same outfit and share a stump; two outfits with identical bodies and
// different names are different outfits and do not.
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
		// Reuse it only if it MINTED. A stump whose directory exists and whose
		// birth record never landed is indistinguishable from a finished one by
		// name alone, and reusing it hands every aria beneath an empty prefix -
		// no skills, no credo: for as long as the store lives. Existence is
		// not completeness.
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
	// The outfit's birth message is renderable (RoleInput, empty content): its
	// form patch renders as the outfit's <system-reminder> blocks ONCE
	// in this shared prefix, inherited (cached) by every conversation.
	stamped := withOutfitVersion(named, ver)
	if err := s.writeStumpBirth(stump, &stamped); err != nil {
		return "", err
	}
	return stump, nil
}

// ForkWith is the ONE birth verb: fork a node and land a patch on the child, in
// one critical section.
//
// parent == "" forks the null root: which is what `fig new` is. A non-empty
// parent branches that aria: at.MainLT == 0 takes the head, otherwise the
// interior point (cauterizing to a fresh child when that point is owned by the
// root or a stump, which is what ForkAt already decides). A FORM parent spawns
// a NEW trunk beneath the live form: binding: because ForkTail on a form
// would be a continuation, and a form is a forking point, not a conversation
// to continue. The form stays appendable; the child snapshots it at its tail.
//
// THE PATCH IS REQUIRED. A fork that transforms nothing is a fork nobody can
// name: the child's identity IS the hash of the patch it was born carrying, and
// at minimum that patch re-stamps aria_id, because a child inheriting its
// parent's id cannot fork itself afterwards.
//
// The patch is appended BEFORE the child's first main record, and that order is
// the whole point. A main record carries a cursor stamp: where each unkeyed
// channel stood when it was written, and the projection renders exactly the
// patches at or below it. Writing the record first stamped it one index BELOW
// the patch it introduces, so nothing rendered: no skills, no credo, nothing the
// birth patch set. This function is now the only place that ordering lives.
func (s *XwalStore) ForkWith(parent string, atMainLT uint64, patch message.Patch) (child string, version uint64, err error) {
	return s.forkWithKind(parent, atMainLT, patch, string(kindConversation))
}

// CreateForm mints an UNBOUND FORM: parent "" forks the null root, a form
// parent duplicates that form's state into a fresh @id. Forms have no
// interior points to fork at: their timeline is one ceremonial record -
// so there is no atMainLT here. Only forms fork independently: a
// conversation parent is refused, because a bound form's fork is the
// aria's fork and it goes through ForkWith.
func (s *XwalStore) CreateForm(parent string, patch message.Patch) (id string, version uint64, err error) {
	if parent != "" {
		s.mu.Lock()
		kind, known := s.trunks.Kind(parent)
		legacyStump := s.isStumpLocked(parent)
		s.mu.Unlock()
		if !legacyStump && (!known || kind != string(kindForm)) {
			return "", 0, fmt.Errorf("xwal store: create form: parent %s is not an unbound form", parent)
		}
	}
	return s.forkWithKind(parent, 0, patch, string(kindForm))
}

// forkWithKind is the shared birth mechanics: pick the spawn shape from
// the parent's species, then land the birth patch and its cursor-stamped
// record in order. Caller chooses what species the CHILD is.
func (s *XwalStore) forkWithKind(parent string, atMainLT uint64, patch message.Patch, kind string) (child string, version uint64, err error) {
	if patch.IsEmpty() {
		return "", 0, fmt.Errorf("xwal store: fork-with: a fork must carry a patch")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	switch {
	case parent == "":
		child, err = s.trunks.SpawnUnderRootKind(kind)
	case s.isStumpLocked(parent):
		// A stump is not a trunk and has no tail to fork: spawning beneath it
		// is what "fork the outfit" means, and the child inherits the birth
		// record every sibling reads. Legacy stumps remain bindable: they
		// were always forms in spirit, and now in name.
		child, err = s.trunks.SpawnUnderStumpKind(parent, kind)
	case s.isFormLocked(parent):
		// A live, patchable forking point: spawn a NEW trunk beneath it.
		// The form is not written, not frozen, and later patches to it
		// belong to the form alone (proved in figwal's spawnkind tests).
		child, err = s.trunks.SpawnChildKind(parent, kind)
	case atMainLT == 0:
		child, err = s.trunks.ForkTail(parent)
	default:
		child, err = s.forkAtLocked(parent, atMainLT)
	}
	if err != nil {
		return "", 0, err
	}
	version, err = s.writeBirth(child, patch)
	if err != nil {
		return "", 0, err
	}
	return child, version, nil
}

// isFormLocked reports whether an id names an unbound form. Caller holds s.mu.
func (s *XwalStore) isFormLocked(id string) bool {
	kind, ok := s.trunks.Kind(id)
	return ok && kind == string(kindForm)
}

// writeBirth appends a node's birth patch and the renderable record that carries
// its cursor stamp. Caller holds s.mu.
func (s *XwalStore) writeBirth(node string, patch message.Patch) (uint64, error) {
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
	gen, _ := json.Marshal(message.Message{Role: message.RoleInput, Timestamp: s.now()})
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

// KeepStump names the one stump collection spares: the current default. The
// angelus sets it whenever it mints or reuses one, so "current" tracks the
// outfit's content rather than its name.
func (s *XwalStore) KeepStump(id string) {
	s.keepStump.Store(&id)
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

// CreateConversation spawns a conversation from an outfit stump.
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

// Fork branches a conversation at its head. The aria id is STABLE: the trunk
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

// ForkAt branches at an interior main-LT (imperative: no message): shares
// [1..atMainLT], mints an empty alternative diverging at atMainLT+1; the id is
// stable (cont == id). At/past the tail it degenerates to a tail fork.
//
// Cauterization: if atMainLT is owned by the root or an outfit stump, it is
// NOT re-split into a continuation, a fresh conversation is spawned beneath
// the owner (an outfitless conversation under the root, or one sharing that
// outfit). Forking a conversation's own turns (or a parent conversation's)
// re-splits normally.
func (s *XwalStore) ForkAt(id string, atMainLT uint64) (cont, alt string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	alt, err = s.forkAtLocked(id, atMainLT)
	if err != nil {
		return "", "", err
	}
	return id, alt, nil
}

// forkAtLocked is ForkAt's decision, shared with ForkWith. Caller holds s.mu.
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

// Promote raises an aria in the PRESENTATION hierarchy. It edits the trunk
// pstate and writes nothing to any aria's history, so it is O(1) in history
// length. ErrAtStump means there is nothing above to promote into, or the
// build has no trunk capability at all.
//
// The climb stops at the outfit boundary. Only conversations nest: an
// outfit stump and the genesis root are structure, and a hierarchy that
// hung one of them under a conversation would put every aria in the store
// inside one aria's subtree.
func (s *XwalStore) Promote(id string, levels int) (int, error) {
	// NO s.mu here: the tree resolves lineage through Node, which refreshes
	// the topology snapshot under s.mu. Holding it across the tree call
	// deadlocks. The tree carries its own lock and its write is atomic.
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

// promotableInto reports whether id currently sits under a conversation.
func (s *XwalStore) promotableInto(id string) bool {
	parent, ok := s.tree.Parent(id)
	if !ok || parent == "" {
		return false
	}
	node, ok := s.Node(parent)
	return ok && node.Kind == string(kindConversation)
}

// OwnerOf resolves which node owns atMainLT along a trunk's lineage (a trunk,
// an outfit stump, or the root): for the <trunk>:<LT> addressing announcement.
func (s *XwalStore) OwnerOf(id string, atMainLT uint64) (xwal.Owner, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.trunks.Owner(id, atMainLT)
}

// writeStumpBirth appends an outfit stump's renderable birth message (IR +
// form stamp). Caller holds s.mu.
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
	// when the record was written. The outfit's reminders are meant to
	// render at THIS record -- once, in the prefix every conversation under
	// the stump inherits -- and the projection renders exactly the patches at
	// or below the record's stamp. Writing the record first stamped it one
	// index BELOW the patch it introduces, so PatchesUpTo() returned nothing
	// and no aria created under the stump ever rendered its skills, its credo
	// or anything else the outfit sets.
	//
	// The patch is keyed to the LT the birth record is about to take, which is
	// what it was keyed to before (the record's own LT) and is the reducible
	// one-ahead convention the flush coherence rule already allows. On an
	// unkeyed form the key is ignored and the stamp is what matters;
	// keying it correctly keeps the keyed case honest rather than relying on
	// the channel happening to be unkeyed.
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
		// Nothing else may write to a stump: CreateOutfit holds s.mu and the
		// stump is minted here. If this ever fires, the patch is keyed to a
		// record that does not exist and the reminders would render against
		// the wrong turn.
		return fmt.Errorf("xwal store: stump %s birth record landed at %d, board patch keyed to %d",
			stump, glt, next)
	}
	// Birth records must be durable before conversations spawn under the
	// stump, a crash between spawn and the next flush would orphan the
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

// OutfitVersion is an outfit's IDENTITY: the value-stable hash of the birth
// record it writes, minus the version field, which cannot cover its own hash.
//
// The name is inside the hash, not merely alongside it. That is what lets
// everything else key on content alone: where a literal sits in a spec changes
// the fold and not the identity, so `base,x=1` and `x=1,base` are one outfit
// when they do not collide -- while `a` and `b` with byte-identical bodies stay
// two outfits, because a listing that reported an aria under a name nobody
// asked for would be worse than a duplicate stump.
func OutfitVersion(name string, patch message.Patch) (string, error) {
	return contentVersion(withOutfitName(patch, name))
}

// LegacyOutfitVersion is OutfitVersion as it was before the name joined the
// hash. It exists so a listing does not call every aria minted by an older
// build stale; delete it when no store in use still holds a stump from one.
func LegacyOutfitVersion(patch message.Patch) (string, error) {
	return contentVersion(patch)
}

// ContentVersion is the value-stable content hash of a patch: an aria's identity
// is the hash of the patch it was born carrying.
func ContentVersion(patch message.Patch) (string, error) { return contentVersion(patch) }

// contentVersion is the value-stable content hash of a patch.
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
	set := make(map[string]json.RawMessage, len(p.Set)+1)
	for k, v := range p.Set {
		set[k] = v
	}
	b, _ := json.Marshal(value)
	set[key] = b
	return message.Patch{Set: set, Remove: p.Remove}
}

// NodeView is a read-only snapshot of an aria (trunk) for listing/lineage.
//
// It carries no `Frozen`/`Children`/`Depth`: those belonged to figaro's own
// pre-trunk tree, where forking froze the target into a read-only index
// node and minted two fresh children. Since the trunk migration the aria id
// is stable: the continuation IS the aria you forked: so no aria is ever
// frozen, and node-level children/depth are figwal's business, not a
// listing's.
type NodeView struct {
	ID     string
	Parent string
	Kind   string
	// Stump is the outfit node this conversation was BORN under, carried down
	// the lineage by the topology walk. Not the presentation parent: a promote
	// moves where a row appears, never where its data came from.
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

// view renders a live (conversation) trunk. Its parent for the global
// hierarchy is its outfit stump (top-level) or its parent conversation trunk
// (a branch); an outfitless top-level trunk hangs off the root.
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
	// The trunk's OWN kind, from its figwal marker: form trunks joined
	// conversations in the tree, and hardcoding conversation here is how
	// a form once got an agent woken for it. Legacy markers all say
	// conversation already; empty (never written) falls back to it.
	kind := t.Kind
	if kind == "" {
		kind = string(kindConversation)
	}
	return NodeView{
		ID: t.ID, Parent: parent, Kind: kind, Trunk: t.ID,
		Stump: p.stump, Vector: p.vec, BranchedLT: t.BranchedLT,
	}
}

// vectorsLocked assigns each conversation trunk its fork-tree vector: the
// child-index path among conversation trunks: roots are [0],[1],…, a branch
// is parentVec+[k]. Siblings are ordered by id (stable; display re-sorts by
// recency). The trunk list is passed in so callers compute it once per
// request (it costs a full disk scan). Caller holds mu.
// place is where a trunk sits: its fork-tree vector, and the stump it was
// born under. One map for both, because they are found by the same walk and
// a second map of the same keys is a second allocation per node.
type place struct {
	vec   []int
	stump string
}

func (s *XwalStore) vectorsLocked(infos []xwal.TrunkInfo) map[string]place {
	live := make(map[string]bool, len(infos))
	for _, ti := range infos {
		// Forms are not part of the conversation fork-tree: a form-born
		// aria is a TOP-LEVEL conversation (its born-of shows in the
		// listing's OUTFIT column), not a branch of an invisible parent -
		// which is exactly how a vector under a never-listed node made
		// bound figaros vanish from `fig ls`.
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
	// One walk, one map. The stump rides alongside the vector: figwal names it
	// only for a trunk rooted directly at one, so a branch inherits it from the
	// trunk it forked: down the LINEAGE edge these kids were built from, the
	// only edge allowed to decide where an aria's data came from (internal/topo).
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

// presentLocked stamps every node's display parent, and the vectors that
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

// presentVectors is vectorsLocked over the display edges: the same rule
// (a conversation under a conversation is a branch, anything else is a
// root), walked once.
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
	// Keyed on BOTH clocks: figwal's topology version and the presentation
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
		// Split the tree by species: `fig ls` lists conversations, and a
		// form leaking in would be an aria-shaped row for a thing with no
		// turns. Forms get their own accessor and the global view carries
		// both.
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

// Conversations returns a view of every conversation trunk, including
// fork-tree vectors but excluding ceremonial anchors.
func (s *XwalStore) Conversations() []NodeView {
	return append([]NodeView(nil), s.topologySnapshot().conversations...)
}

// Forms returns every unbound form trunk.
func (s *XwalStore) Forms() []NodeView {
	return append([]NodeView(nil), s.topologySnapshot().forms...)
}

// ConversationIDs returns persisted conversation ids without computing
// vectors or reading ceremonial outfit anchors.
func (s *XwalStore) ConversationIDs() []string {
	return append([]string(nil), s.topologySnapshot().conversationIDs...)
}

// Nodes returns a view of every conversation trunk plus the ceremonial
// anchors (the root + every outfit stump).
func (s *XwalStore) Nodes() []NodeView {
	return append([]NodeView(nil), s.topologySnapshot().nodes...)
}

// Node returns a single trunk view (incl. the root + outfit stumps).
func (s *XwalStore) Node(id string) (NodeView, bool) {
	node, ok := s.topologySnapshot().byID[id]
	return node, ok
}

// RemoveLeaf deletes an aria via xwal.Trunks. Trunk-addressed; refuses one
// with live branches unless recursive.
//
// The two hierarchies split the work. What is REFUSED is counted on the
// drawn tree, so the warning matches what `fig ls` shows. What is REMOVED
// is the history subtree, because that is what owns bytes. An aria merely
// promoted under the target therefore survives, and forgetting its edge
// returns it to where its history puts it.
//
// Survivors that read their history through the delete set absorb the
// prefix they borrow BEFORE anything is unlinked, so a crash between the
// two leaves them reading through directories still present.
// bury, when non-nil, is handed the whole delete set AFTER the refusal and
// BEFORE any repair or unlink. It is where the dying forms record their own
// death (durable-forms §7): the record must precede the unlink, or a crash
// between them leaves a live-looking form whose files are half gone.
func (s *XwalStore) RemoveLeaf(id string, recursive bool, bury func([]string)) error {
	s.deleting.Lock()
	defer s.deleting.Unlock()
	// Refuse before touching anything. The boundary repair below rewrites
	// surviving arias, so a delete that is going to be refused must be
	// refused while the store still looks the way the caller found it.
	taken := s.tree.DeleteSet(id)
	if !recursive && len(taken) > 1 {
		return fmt.Errorf("%w: %q has %d; -r takes them too", ErrHasBranches, id, len(taken)-1)
	}
	if bury != nil {
		bury(taken)
	}
	// Repair the boundary FIRST: every survivor that reads its history
	// through this delete set absorbs that prefix and stops pointing at it.
	// Only then does anything get unlinked, so a crash between the two
	// leaves survivors that still read through directories still present.
	homes := map[string]string{}
	// Anything DRAWN under something this delete takes has to be re-drawn
	// somewhere that survives. Its edge is about to be forgotten, and
	// falling back to the topology puts it under the genesis root with no
	// outfit, which is the fossil this whole path exists to stop making.
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
		// Where it is DRAWN today, remembered before the detach empties its
		// .from. Absorbing a prefix is a storage repair; without this it
		// also teleports an untouched aria to the genesis root.
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
	// Which stump hosts it must be read BEFORE the removal (afterwards the
	// topology no longer knows the two were related) and before the lock:
	// topologySnapshot takes s.mu itself.
	stump := s.topologySnapshot().byID[id].Stump
	if err := s.removeLocked(id, recursive); err != nil {
		return err
	}
	// Presentation is repaired with s.mu RELEASED: an edge resolves through
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
	// The stump goes only if nothing is left wearing it. A detached
	// survivor no longer counts as a child in the topology, so collecting
	// on that count alone took the outfit out from under an aria that is
	// still drawn beneath it.
	if !keep {
		s.collectStumpAfterDelete(stump)
	}
	return nil
}

// removeLocked is the unlink, the only part of a delete that touches the
// store. Stump collection is deferred: it depends on where the survivors
// end up.
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

// survivingHome is the nearest place above id that outlives this delete: the
// first drawn ancestor still standing, or failing that the outfit it was
// born under.
//
// The fallback is the difference between a top-level aria and a fossil. A
// detach empties the node's .from, and with it figwal's record of which
// stump the aria hangs from, so an aria whose whole lineage was deleted
// would be drawn directly under the genesis root with no outfit above it -
// which is exactly the shape a store full of old recursive kills is in.
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

// CollectStump removes a childless outfit stump. Refuses one still hosting
// arias: the caller is expected to have checked, so a refusal is a race.
func (s *XwalStore) CollectStump(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.trunks.RemoveStump(id)
}

// collectStump removes a stump that has just lost its last child.
//
// An outfit stump is content-addressed, so it is minted afresh
// by the next aria that wants it: collecting one loses nothing and is what
// keeps a store from accumulating a directory per outfit version forever. A
// recursive delete can take several children at once, which is why this asks
// the topology rather than counting: whatever is left is what is left.
//
// A failure here is logged, not returned: the aria IS deleted by this point,
// and failing the delete because the collection failed would be a lie.
func (s *XwalStore) collectStump(name string) {
	if name == "" {
		return
	}
	// The LIVE default is kept even when childless: it is the one stump the
	// next `fig new` will want, and re-minting it means re-writing the whole
	// outfit: every skill, every credo: for nothing. Superseded versions of
	// the same outfit are not spared: the hash is what varies when the files
	// change, so keeping "the default" by name would pin every version it ever
	// had.
	keep := ""
	if p := s.keepStump.Load(); p != nil {
		keep = *p
	}
	if name == keep {
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

// LastTS is the newest figwal record timestamp anywhere in a node, unix
// millis: recency for listings. figwal serves it from the open handle's
// lock-free counter (one head open hydrates a cold node, and the trunks
// layer keeps it warm). This NEVER wakes an agent: it opens a store
// handle, not a figaro. Zero for pre-timestamp history: "we can
// tolerate without them".
func (s *XwalStore) LastTS(id string) int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Trunk first, BY MAP LOOKUP: the first draft asked isStumpLocked
	// before anything, and that is a full stump scan: per row, so a
	// 300-aria listing went O(n²) and the -count=6 battery caught it at
	// +6784% (18ms where 262µs stood). Kind() is an index map hit; the
	// stump fallback (StumpLastTS guards with a node map hit) only runs
	// for ids that are not trunks at all.
	if _, ok := s.trunks.Kind(id); ok {
		return s.trunks.LastTS(id)
	}
	return s.trunks.StumpLastTS(id)
}

// KindForm is the public name of the unbound-form node kind, for
// consumers that discriminate rows by species.
const KindForm = string(kindForm)

// ---- the observed set (study subscriptions, pull-at-the-stamp) ----

// librettoCursorPrefix namespaces observed-form positions inside the main
// record's cursor map, beside the node's own channel entries. The value is
// the LIBRETTO's version, never the source's.
//
// Records written before librettos existed carry the older "study:" prefix
// holding SOURCE versions. Those no longer match, so they render no study
// block at all -- which is the point: reading a source version against a
// libretto's log answers a wrong range silently.
const librettoCursorPrefix = "libretto:"

// studyCursors extracts the observed-form half of a cursor stamp,
// keyed by bare form id.
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

// SetObservedForms declares which forms an aria observes; every
// subsequent IR append stamps their positions. The list is the AGENT's
// declaration (mirroring its board's system.studies): the store holds
// it in memory only, because the board is the durable truth and the
// agent re-declares on boot.
func (s *XwalStore) SetObservedForms(ariaID string, formIDs []string) {
	s.observedMu.Lock()
	if len(formIDs) == 0 {
		delete(s.observed, ariaID)
	} else {
		s.observed[ariaID] = append([]string(nil), formIDs...)
	}
	s.observedMu.Unlock()
}

// observedCursors reads each observed form's LIBRETTO version at the stamp
// moment. The source form is never touched: the libretto is the copy the
// translator renders from, and it outlives its source.
func (s *XwalStore) observedCursors(ariaID string) map[string]uint64 {
	s.observedMu.Lock()
	ids := s.observed[ariaID]
	s.observedMu.Unlock()
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

// formTail is the form channel's last index for any node: the version
// a conditional Set quotes, read from the hot handle without a Form
// replay.
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

// HandleIdleForTest and the two beside it expose the ENFORCEMENT POINTS to
// the settings test in internal/cli. They read the package variables the
// store consults when it opens a handle, builds a form, or trims one, which
// is the whole point: a config test that stops at the accessor cannot tell a
// wired knob from an unwired one.
func HandleIdleForTest() time.Duration { return time.Duration(handleIdle.Load()) }
