package rpc

type GrantsRequest struct {
	FigaroID string `json:"figaro_id"`
}

// Effective is what figaro enforces and what a provider should read: the
// raw system.grants key is only this figaro's own narrowing. No grant here
// has a * verb: figaro's verbs are spelled out one by one, and a verb that
// is not figaro's (egress, cred) appears only where someone wrote it.
type GrantsResponse struct {
	FigaroID   string        `json:"figaro_id"`
	Effective  []string      `json:"effective"`
	Ceiling    []string      `json:"ceiling,omitempty"`
	CeilingSet bool          `json:"ceiling_set"`
	Chain      []GrantsLevel `json:"chain"`
}

type GrantsLevel struct {
	FigaroID   string   `json:"figaro_id"`
	Grants     []string `json:"grants,omitempty"`
	Set        bool     `json:"set"`
	Unreadable bool     `json:"unreadable,omitempty"`
}
