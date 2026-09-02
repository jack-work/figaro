package rpc

// TOPOLOGY: birth, fork, promote, import, and removal.
//
// One family per file: the surface is legible when a reader can see a whole
// family at once, and the May 2026 tightening drifted partly because 40
// method names and 70 types shared one 1,012-line file.

import (
	"github.com/jack-work/figaro/api/message"
)

// CreateRequest names the outfit for a new aria. The system mints the
// aria id; callers cannot choose it.
// CreateRequest mints an aria from a patch. An empty patch means the
// configured default_outfit, which the angelus assembles; a patch that arrives
// is folded ON TOP of that default, so `-O mantra=x` adds rather than replaces.
type CreateRequest struct {
	Patch *FormPatch `json:"patch,omitempty"`
	// Outfits are outfit NAMES, folded in order and UNDER Patch's own keys.
	// The outfit axis is separate from the patch axis: names here, data
	// there, and the daemon's ONE dressing call at the API boundary is what
	// turns the first into the second. Nothing below that boundary reads a
	// file.
	Outfits []string `json:"outfits,omitempty"`
	// Cwd is the caller's working directory. Empty falls back to the daemon's.
	Cwd string `json:"cwd,omitempty"`
}

type CreateResponse struct {
	FigaroID string   `json:"figaro_id"`
	Endpoint Endpoint `json:"endpoint"`
}

// FormCreateRequest mints an unbound form. Parent "" forks the null root;
// a form id duplicates that form's state into a fresh @id. The patch is
// required, a fork that transforms nothing is a fork nobody can name.
type FormCreateRequest struct {
	Parent string     `json:"parent,omitempty"`
	Patch  *FormPatch `json:"patch"`
	// Outfits are outfit NAMES, folded in order and UNDER Patch's own keys.
	// The outfit axis is separate from the patch axis: names here, data
	// there, and the daemon's ONE dressing call at the API boundary is what
	// turns the first into the second. Nothing below that boundary reads a
	// file.
	Outfits []string `json:"outfits,omitempty"`
}

type FormCreateResponse struct {
	FormID   string   `json:"form_id"`
	Version  uint64   `json:"version"`
	Endpoint Endpoint `json:"endpoint"`
}

// FormBindRequest births a figaro from a form. Parent "" (or "null")
// forks the null root: the naked figaro. Patch is the optional -O
// dressing; unlike form.create it may be absent (runtime fill-ins make
// the birth patch nonempty).
type FormBindRequest struct {
	Parent string     `json:"parent,omitempty"`
	Patch  *FormPatch `json:"patch,omitempty"`
	// Outfits are outfit NAMES, folded in order and UNDER Patch's own keys.
	// The outfit axis is separate from the patch axis: names here, data
	// there, and the daemon's ONE dressing call at the API boundary is what
	// turns the first into the second. Nothing below that boundary reads a
	// file.
	Outfits []string `json:"outfits,omitempty"`
	// Cwd is the caller's working directory. Empty falls back to the daemon's.
	Cwd string `json:"cwd,omitempty"`
}

type FormBindResponse struct {
	FigaroID string   `json:"figaro_id"`
	Endpoint Endpoint `json:"endpoint"`
}

// ForkRequest branches a conversation. With neither coordinate set it forks
// at the head.
type ForkRequest struct {
	FigaroID string `json:"figaro_id"`
	// AtTurn is the turn to REPLACE, the coordinate a human names
	// (`fig fork <id>:12`) and the one `fig show` prints. Turn N's fork
	// point is the LT that ENDS turn N-1, so the branch retains everything
	// through the previous exchange and the new prompt becomes turn N.
	AtTurn uint64 `json:"at_turn,omitempty"`
	// AtLT is the same request in the MODEL's coordinate: the IR logical
	// time to fork at, exactly (`fig fork <id>.42` -- the dot form). It is
	// for callers that already hold an LT, and for forking where no turn
	// boundary exists, which a turn cannot express.
	AtLT uint64 `json:"at_lt,omitempty"`
	// Patch dresses the ALTERNATIVE the moment it exists, before anything is
	// said to it: it lands on the child's form in the same call that
	// mints it, so a prompt sent next is answered with it in place.
	Patch *FormPatch `json:"patch,omitempty"`
	// Outfits are outfit NAMES, folded in order and UNDER Patch's own keys.
	// The outfit axis is separate from the patch axis: names here, data
	// there, and the daemon's ONE dressing call at the API boundary is what
	// turns the first into the second. Nothing below that boundary reads a
	// file.
	Outfits []string `json:"outfits,omitempty"`
}

// ForkResponse returns the two fresh child ids. The parent freezes and
// keeps its id as a navigable (read-only) index node. OwnerNote, when set,
// announces that an interior <id>:<LT> resolved to an owning ancestor (a
// parent trunk, an outfit, or the genesis root) and what was branched there.
type ForkResponse struct {
	Parent       string `json:"parent"`
	Continuation string `json:"continuation"`
	Alternative  string `json:"alternative"`
	OwnerNote    string `json:"owner_note,omitempty"`
}

// NormalizeRequest forces deferred topology work to run now. It is the one
// blocking operation in the trunk surface: everything else is instant
// because this can be postponed.
type NormalizeRequest struct {
	// Segments also repacks partially filled segment files. Not yet
	// implemented; the server reports it rather than silently ignoring it.
	Segments bool `json:"segments,omitempty"`
}

// NormalizeResponse reports how many arias were made independent of the
// ancestors they are no longer presented under.
type NormalizeResponse struct {
	Detached    int  `json:"detached"`
	Unsupported bool `json:"unsupported,omitempty"`
}

// PromoteRequest climbs a conversation trunk up Levels stump-bounded levels,
// relabeling the canonical trunk path so it absorbs its parent trunk's run.
type PromoteRequest struct {
	FigaroID string `json:"figaro_id"`
	Levels   int    `json:"levels,omitempty"`
}

// PromoteResponse reports how many levels the aria actually climbed.
// AtStump is true when it could not climb at all: it is already at the top
// of its presentation hierarchy, or the server has no trunk capability and
// therefore no hierarchy to edit (Unsupported).
type PromoteResponse struct {
	FigaroID string `json:"figaro_id"`
	Climbed  int    `json:"climbed"`
	AtStump  bool   `json:"at_stump,omitempty"`
	// Unsupported reports a server built without the trunk capability.
	Unsupported bool `json:"unsupported,omitempty"`
}

// ImportRequest restores an exported aria into this store as a NEW
// conversation. Nothing is grafted: the outfit is resolved or created by
// content, a fresh conversation is spawned under it, and Messages are appended
// through the ordinary path: so node ids, fork bases and LTs are the
// destination's own and can never collide with what is already there.
type ImportRequest struct {
	Outfit      string            `json:"outfit"`
	OutfitPatch message.Patch     `json:"outfit_patch,omitempty"`
	Form        message.Patch     `json:"form,omitempty"`
	Messages    []message.Message `json:"messages"`
	WasID       string            `json:"was_id,omitempty"`
	Mantra      string            `json:"mantra,omitempty"`
	Provider    string            `json:"provider,omitempty"`
	Model       string            `json:"model,omitempty"`
}

// ImportResponse names the aria that was created, and what it used to be.
type ImportResponse struct {
	FigaroID string `json:"figaro_id"`
	Outfit   string `json:"outfit"`
	Messages int    `json:"messages"`
	WasID    string `json:"was_id,omitempty"`
}

// GCRequest asks the angelus to collect outfit stumps nothing is using.
type GCRequest struct {
	// DryRun reports what would go without removing anything.
	DryRun bool `json:"dry_run,omitempty"`
}

// GCStump is one outfit stump and its fate.
type GCStump struct {
	ID       string `json:"id"`
	Outfit   string `json:"outfit,omitempty"`
	Version  string `json:"version,omitempty"`
	Children int    `json:"children"`
	// Collected is true when it was removed, or: under DryRun: would be.
	Collected bool   `json:"collected,omitempty"`
	Err       string `json:"err,omitempty"`
}

// GCResponse lists every stump considered, in name order.
type GCResponse struct {
	Stumps    []GCStump `json:"stumps"`
	Collected int       `json:"collected"`
	DryRun    bool      `json:"dry_run,omitempty"`
}

type KillRequest struct {
	FigaroID  string `json:"figaro_id"`
	Recursive bool   `json:"recursive,omitempty"` // also remove the trunk's live branches
}

type KillResponse struct {
	OK bool `json:"ok"`
}
