package rpc

type ScopesRequest struct {
	FigaroID string `json:"figaro_id"`
}

// Effective is what figaro enforces and what a provider should read: the
// raw system.scopes key is only this figaro's own narrowing. No entry here
// has a * verb: figaro's verbs are spelled out one by one, and a verb that
// is not figaro's (egress, cred) appears only where someone wrote it.
type ScopesResponse struct {
	FigaroID   string        `json:"figaro_id"`
	Effective  []string      `json:"effective"`
	Ceiling    []string      `json:"ceiling,omitempty"`
	CeilingSet bool          `json:"ceiling_set"`
	Chain      []ScopesLevel `json:"chain"`
}

// Grants repeats Scopes under its old name for readers of figaro.grants.
type ScopesLevel struct {
	FigaroID   string   `json:"figaro_id"`
	Scopes     []string `json:"scopes,omitempty"`
	Grants     []string `json:"grants,omitempty"`
	Set        bool     `json:"set"`
	Unreadable bool     `json:"unreadable,omitempty"`
}

type (
	GrantsRequest  = ScopesRequest
	GrantsResponse = ScopesResponse
	GrantsLevel    = ScopesLevel
)
