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

// NormalizeRequest forces deferred topology work to run now.
type NormalizeRequest struct {
	// Segments is not yet implemented; the server reports it as unsupported.
	Segments bool `json:"segments,omitempty"`
}

type NormalizeResponse struct {
	Detached    int  `json:"detached"`
	Unsupported bool `json:"unsupported,omitempty"`
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

// GCRequest collects outfit versions nothing is using.
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

// GCResponse lists every outfit version considered, in name order.
type GCResponse struct {
	Stumps    []GCStump `json:"stumps"`
	Collected int       `json:"collected"`
	DryRun    bool      `json:"dry_run,omitempty"`
}

type KillRequest struct {
	FigaroID  string `json:"figaro_id"`
	Recursive bool   `json:"recursive,omitempty"` // also remove the trunk's live branches
	Now       bool   `json:"now,omitempty"`
	GraceMS   int64  `json:"grace_ms,omitempty"`
}

type KillResponse struct {
	OK      bool  `json:"ok"`
	PurgeAt int64 `json:"purge_at,omitempty"`
}
