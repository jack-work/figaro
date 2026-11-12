package rpc

import (
	"github.com/jack-work/figaro/api/message"
)

// CreateRequest mints an aria; the system chooses the id. Patch is folded on
// top of the configured default_outfit.
type CreateRequest struct {
	Patch *FormPatch `json:"patch,omitempty"`
	// Outfits are names, folded in order and under Patch's own keys.
	Outfits []string `json:"outfits,omitempty"`
	// Cwd empty falls back to the daemon's.
	Cwd string `json:"cwd,omitempty"`
}

type CreateResponse struct {
	FigaroID string `json:"figaro_id"`
}

// FormCreateRequest mints an unbound form. Parent "" spawns under the null root; a
// form id duplicates that form's state. Patch is required.
type FormCreateRequest struct {
	Parent  string     `json:"parent,omitempty"`
	Patch   *FormPatch `json:"patch"`
	Outfits []string   `json:"outfits,omitempty"`
}

type FormCreateResponse struct {
	FormID  string `json:"form_id"`
	Version uint64 `json:"version"`
}

// FormBindRequest births a figaro from a form. Parent "" or "null" forks the
// null root. Patch may be absent.
type FormBindRequest struct {
	Parent  string     `json:"parent,omitempty"`
	Patch   *FormPatch `json:"patch,omitempty"`
	Outfits []string   `json:"outfits,omitempty"`
	Cwd     string     `json:"cwd,omitempty"`
}

type FormBindResponse struct {
	FigaroID string `json:"figaro_id"`
}

// ForkRequest with neither coordinate set forks at the head.
type ForkRequest struct {
	FigaroID string `json:"figaro_id"`
	// AtTurn is the turn to replace (`fork <id>:12`): the fork point is the
	// LT that ends turn N-1, and the new prompt becomes turn N.
	AtTurn uint64 `json:"at_turn,omitempty"`
	// AtLT is the exact IR logical time (`fork <id>.42`), for forking where
	// no turn boundary exists.
	AtLT uint64 `json:"at_lt,omitempty"`
	// Patch dresses the alternative in the same call that mints it.
	Patch   *FormPatch `json:"patch,omitempty"`
	Outfits []string   `json:"outfits,omitempty"`
}

// ForkResponse: the parent freezes and keeps its id as a read-only index
// node. OwnerNote announces that an interior <id>:<LT> resolved to an owning
// ancestor.
type ForkResponse struct {
	Parent       string `json:"parent"`
	Continuation string `json:"continuation"`
	Alternative  string `json:"alternative"`
	OwnerNote    string `json:"owner_note,omitempty"`
}

type NormalizeRequest struct {
	DryRun bool `json:"dry_run,omitempty"`
}

type NormalizedAria struct {
	ID     string `json:"id"`
	Handle string `json:"handle,omitempty"`
}

type DroppedOverride struct {
	NormalizedAria
	Under       string `json:"under"`
	UnderHandle string `json:"under_handle,omitempty"`
	Reason      string `json:"reason"`
}

type NormalizeResponse struct {
	DryRun      bool              `json:"dry_run,omitempty"`
	Detached    []NormalizedAria  `json:"detached"`
	Dropped     []DroppedOverride `json:"dropped"`
	Unsupported bool              `json:"unsupported,omitempty"`
}

type RestoreForksRequest struct {
	DryRun bool `json:"dry_run,omitempty"`
}

// RestoredFork is a figaro an older release moved by rewriting its fork
// point. Was is the figaro it names today and keeps answering to; ForkPoint
// is the holder of its cut, empty when Kept says why it was left alone.
type RestoredFork struct {
	NormalizedAria
	Was             string `json:"was"`
	WasHandle       string `json:"was_handle,omitempty"`
	ForkPoint       string `json:"fork_point,omitempty"`
	ForkPointHandle string `json:"fork_point_handle,omitempty"`
	Kept            string `json:"kept,omitempty"`
}

type RestoreForksResponse struct {
	DryRun   bool           `json:"dry_run,omitempty"`
	Restored []RestoredFork `json:"restored"`
	Kept     []RestoredFork `json:"kept"`
}

// PlacedAria is a figaro whose placement or fork point needs its owner's
// eye. Names is the store's name for a node that is gone.
type PlacedAria struct {
	NormalizedAria
	ForkPoint       string `json:"fork_point,omitempty"`
	ForkPointHandle string `json:"fork_point_handle,omitempty"`
	Under           string `json:"under,omitempty"`
	UnderHandle     string `json:"under_handle,omitempty"`
	Names           string `json:"names,omitempty"`
}

// PlacementsResponse: Placed answer to a figaro other than their fork point,
// Lost hold nothing because their placement names a node that is gone, and
// Dangling read as top-level because their fork point names no node.
type PlacementsResponse struct {
	Placed   []PlacedAria `json:"placed"`
	Lost     []PlacedAria `json:"lost"`
	Dangling []PlacedAria `json:"dangling"`
}

// PromoteRequest climbs a trunk up to Levels levels, absorbing its
// parent trunk's run; it stops at the outfit version or root it was born under.
type PromoteRequest struct {
	FigaroID string `json:"figaro_id"`
	Levels   int    `json:"levels,omitempty"`
}

// AtStump: already at the top, or no trunk capability (Unsupported).
type PromoteResponse struct {
	FigaroID    string `json:"figaro_id"`
	Climbed     int    `json:"climbed"`
	AtStump     bool   `json:"at_stump,omitempty"`
	Unsupported bool   `json:"unsupported,omitempty"`
}

type DemoteRequest struct {
	FigaroID string `json:"figaro_id"`
	Prefer   string `json:"prefer,omitempty"`
}

type DemoteResponse struct {
	FigaroID    string `json:"figaro_id"`
	Under       string `json:"under,omitempty"`
	Preferred   bool   `json:"preferred,omitempty"`
	Unsupported bool   `json:"unsupported,omitempty"`
}

// ImportRequest restores an exported aria as a new conversation: nothing is
// grafted, so ids and LTs are the destination's own.
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

type ImportResponse struct {
	FigaroID string `json:"figaro_id"`
	Outfit   string `json:"outfit"`
	Messages int    `json:"messages"`
	WasID    string `json:"was_id,omitempty"`
}

type GCRequest struct {
	DryRun bool `json:"dry_run,omitempty"`
}

type GCStump struct {
	ID        string `json:"id"`
	Outfit    string `json:"outfit,omitempty"`
	Version   string `json:"version,omitempty"`
	Children  int    `json:"children"`
	Collected bool   `json:"collected,omitempty"` // under DryRun: would be
	Err       string `json:"err,omitempty"`
}

type GCForm struct {
	ID       string `json:"id"`
	Pins     int    `json:"pins"`
	Segments int    `json:"segments"`
	Bytes    int64  `json:"bytes"`
}

type GCResponse struct {
	Stumps    []GCStump `json:"stumps"`
	Collected int       `json:"collected"`
	DryRun    bool      `json:"dry_run,omitempty"`

	Retention    string   `json:"retention,omitempty"`
	Forms        []GCForm `json:"forms,omitempty"`
	FormsPlanned bool     `json:"forms_planned,omitempty"`
	FormsErr     string   `json:"forms_err,omitempty"`
}

type KillRequest struct {
	FigaroID  string `json:"figaro_id"`
	Recursive bool   `json:"recursive,omitempty"` // also remove the trunk's live branches
	Now       bool   `json:"now,omitempty"`
	GraceMS   int64  `json:"grace_ms,omitempty"`
}

type ReapedRestoreRequest struct {
	FigaroID string `json:"figaro_id"`
	Keep     bool   `json:"keep,omitempty"`
}

type ReapedRestoreResponse struct {
	Root     string   `json:"root"`
	Restored []string `json:"restored"`
}

type ModelsPickRequest struct {
	FigaroID string `json:"figaro_id"`
	Provider string `json:"provider"`
	Model    string `json:"model"`
}

type ModelsPickResponse struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
}

type KillResponse struct {
	OK      bool     `json:"ok"`
	PurgeAt int64    `json:"purge_at,omitempty"`
	Removed []string `json:"removed,omitempty"`
	Roles   []string `json:"roles,omitempty"`
}
