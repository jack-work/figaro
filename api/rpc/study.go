package rpc

// STUDY AND CAST: an aria's subscriptions to unbound forms.
//
// One family per file: the surface is legible when a reader can see a whole
// family at once, and the May 2026 tightening drifted partly because 40
// method names and 70 types shared one 1,012-line file.

import ()

// StudyRequest subscribes (figaro.study) or unsubscribes (figaro.drop)
// the aria from an unbound form. Empty FormID on figaro.study lists.
type StudyRequest struct {
	FormID string `json:"form_id,omitempty"`
}

type StudyResponse struct {
	OK      bool     `json:"ok"`
	Studies []string `json:"studies"`
}

// CastRequest is one casting call. Exactly one of FormID / RolePatch:
// an existing role's id, or the patch a new role is BORN from (the
// server folds target-aria in, so nothing half-fails).
type CastRequest struct {
	FormID    string     `json:"form_id,omitempty"`
	RolePatch *FormPatch `json:"role_patch,omitempty"`
	// Outfits are outfit NAMES, folded in order and UNDER Patch's own keys.
	// The outfit axis is separate from the patch axis: names here, data
	// there, and the daemon's ONE dressing call at the API boundary is what
	// turns the first into the second. Nothing below that boundary reads a
	// file.
	Outfits []string `json:"outfits,omitempty"`
}

// CastResponse reports the call's verdict, step by step, so a partial
// failure is a described state and never a mystery.
type CastResponse struct {
	RoleID  string `json:"role_id"`
	Studied bool   `json:"studied"` // newly studied by this call
	Patched bool   `json:"patched"` // target-aria points here
}
