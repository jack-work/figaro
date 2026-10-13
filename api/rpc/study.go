package rpc

// StudyRequest with an empty FormID on figaro.study lists.
type StudyRequest struct {
	FormID string `json:"form_id,omitempty"`
}

type StudyResponse struct {
	OK      bool     `json:"ok"`
	Studies []string `json:"studies"`
}

// CastRequest: exactly one of FormID (an existing role) or RolePatch (a new
// role; the server folds target-aria in).
type CastRequest struct {
	FormID    string     `json:"form_id,omitempty"`
	RolePatch *FormPatch `json:"role_patch,omitempty"`
	Outfits   []string   `json:"outfits,omitempty"`
}

type CastResponse struct {
	RoleID  string `json:"role_id"`
	Studied bool   `json:"studied"` // newly studied by this call
	Patched bool   `json:"patched"` // target-aria points here
}
