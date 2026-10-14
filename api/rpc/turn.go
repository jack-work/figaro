package rpc

import (
	"encoding/json"

	"github.com/jack-work/figaro/api/message"
)

// ErrorData is the payload attached to typed JSON-RPC errors.
type ErrorData struct {
	AvailableProviders []string `json:"available_providers,omitempty"`
	Outfit             string   `json:"outfit,omitempty"`
	Name               string   `json:"name,omitempty"`
	SearchPaths        []string `json:"search_paths,omitempty"`

	// OutfitClosure accompanies ErrOutfitNotFound; each node reports whether
	// it was found on disk.
	OutfitClosure *OutfitLayer `json:"outfit_closure,omitempty"`
}

type QuaRequest struct {
	Text string     `json:"text"`
	Form *FormInput `json:"form,omitempty"`
}

// FormInput is the client's view of state at send time and the delta it means.
type FormInput struct {
	Context map[string]json.RawMessage `json:"context,omitempty"`
	Patch   *FormPatch                 `json:"patch,omitempty"`
	// Outfits are names, folded in order and under Patch's own keys.
	Outfits []string `json:"outfits,omitempty"`
}

// FormDeltaSchema versions the envelope, not the patch.
const FormDeltaSchema = 1

// FormDelta is the same shape in both directions: what a client sends as
// {patch, if_version} comes back as {patch, version}.
type FormDelta struct {
	Schema int    `json:"schema"`
	AriaID string `json:"aria_id,omitempty"`
	// Intrinsic is the segment after the slash in `<host>/<name>`; empty is
	// `state`, the host's own form.
	Intrinsic string    `json:"intrinsic,omitempty"`
	Version   uint64    `json:"version"`
	Patch     FormPatch `json:"patch"`
	At        int64     `json:"at,omitempty"`
}

type FormPatch = message.Patch

type QuaResponse struct {
	OK bool `json:"ok"`
	// Cursor is the newest materialized turn id: the resume point to read
	// from before following pushes.
	Cursor int `json:"cursor"`
	// Active reports that a turn was already in flight, so the prompt queued
	// or steered rather than starting fresh.
	Active bool `json:"active,omitempty"`
}

type InterruptRequest struct {
	Queue QueueDisposition `json:"queue,omitempty"` // "" == QueueKeep
}

// InterruptResponse.Queue is the queue as of the hangup, always populated;
// Cleared says whether it was removed. Epoch names the inbox generation.
type InterruptResponse struct {
	OK bool `json:"ok"`
	// Stopped says whether there was a turn to stop.
	Stopped bool           `json:"stopped,omitempty"`
	Cleared bool           `json:"cleared,omitempty"`
	Epoch   string         `json:"epoch,omitempty"`
	Queue   []QueuedPrompt `json:"queue,omitempty"`
}

type SetRequest struct {
	Patch   FormPatch `json:"patch"`
	Outfits []string  `json:"outfits,omitempty"`
	// IfVersion refuses the patch unless the board is still at this durable
	// version; zero is unconditional.
	IfVersion uint64 `json:"if_version,omitempty"`
	// Assert makes a removal of an absent key a refusal rather than a no-op.
	Assert bool `json:"assert,omitempty"`
}

type SetResponse struct {
	OK     bool     `json:"ok"`
	Set    []string `json:"set,omitempty"`
	Remove []string `json:"remove,omitempty"`
	// Outcome is empty from a daemon that predates it.
	Outcome string `json:"outcome,omitempty"`
	// Version is the durable version the board stands at after this call,
	// what a follower quotes back in IfVersion.
	Version uint64 `json:"version,omitempty"`
}
