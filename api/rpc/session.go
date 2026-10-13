package rpc

type FigaroInfoResponse struct {
	ID               string `json:"id"`
	State            string `json:"state"`
	Provider         string `json:"provider"`
	Model            string `json:"model"`
	MessageCount     int    `json:"message_count"`
	TokensIn         int    `json:"tokens_in"`
	TokensOut        int    `json:"tokens_out"`
	CacheReadTokens  int    `json:"cache_read_tokens"`
	CacheWriteTokens int    `json:"cache_write_tokens"`
	ContextTokens    int    `json:"context_tokens"`          // estimated next-turn input size
	ContextLimit     int    `json:"context_limit,omitempty"` // when known
	ContextExact     bool   `json:"context_exact"`           // from a Usage watermark
	CreatedAt        int64  `json:"created_at"`              // unix millis
	LastActive       int64  `json:"last_active"`             // unix millis
	AgentSince       int64  `json:"agent_since,omitempty"`   // unix millis; live rows only
	WokeBy           string `json:"woke_by,omitempty"`       // the RPC method that restored a dormant aria
	Mantra           string `json:"mantra"`                  // form "mantra"
	Cwd              string `json:"cwd"`                     // form "system.cwd"
	OutfitName       string `json:"outfit_name,omitempty"`   // form system.outfit_name
	OutfitVer        string `json:"outfit_ver,omitempty"`    // "live" if the stamped hash matches the current outfit, else its short hash
	BoundPIDs        []int  `json:"bound_pids"`

	// TTLMS is board key system.ttl; ExpiresAt is CreatedAt + TTLMS. Zero
	// when no lifetime is stated.
	TTLMS     int64 `json:"ttl_ms,omitempty"`
	ExpiresAt int64 `json:"expires_at,omitempty"`

	// Fork-tree position. Vector is the child-index path; Trunk flows down
	// the continuation line; Parent is the aria branched from.
	Vector     []int  `json:"vector,omitempty"`
	Trunk      string `json:"trunk,omitempty"`
	Parent     string `json:"parent,omitempty"`
	BranchedLT uint64 `json:"branched_lt,omitempty"`
	// Present is where the row is drawn: Parent unless a promote moved it.
	// Vector follows Present.
	Present string `json:"present,omitempty"`
	Kind    string `json:"kind,omitempty"` // "conversation" | "form" | "outfit" | "null"; global listings only

	// Unbound-form rows only; TargetAria is set when the form is a role.
	Name       string `json:"name,omitempty"`
	TargetAria string `json:"target_aria,omitempty"`
}

type Endpoint struct {
	Scheme  string `json:"scheme"`
	Address string `json:"address"`
}

// AttachRequest restores a dormant aria without binding a pid.
type AttachRequest struct {
	FigaroID string `json:"figaro_id"`
}

type AttachResponse struct {
	FigaroID string   `json:"figaro_id"`
	Endpoint Endpoint `json:"endpoint"`
}

// IDsOnly skips the per-aria form and tree fills. Global also lists the
// ceremonial anchors (null root, versioned outfits) with Kind/Parent set.
type ListRequest struct {
	IDsOnly bool `json:"ids_only,omitempty"`
	Global  bool `json:"global,omitempty"`
}

type ListResponse struct {
	Figaros []FigaroInfoResponse `json:"figaros"`
}

type BindRequest struct {
	PID      int    `json:"pid"`
	FigaroID string `json:"figaro_id"`
	AtMainLT uint64 `json:"at_main_lt,omitempty"` // pending fork-point; 0 = leaf
}

type BindResponse struct {
	OK bool `json:"ok"`
}

type ResolveRequest struct {
	PID int `json:"pid"`
}

type ResolveResponse struct {
	FigaroID string   `json:"figaro_id,omitempty"`
	Endpoint Endpoint `json:"endpoint,omitempty"`
	Found    bool     `json:"found"`
	AtMainLT uint64   `json:"at_main_lt,omitempty"` // pending fork-point bound to this pid
}

type UnbindRequest struct {
	PID int `json:"pid"`
}

type UnbindResponse struct {
	OK bool `json:"ok"`
}
