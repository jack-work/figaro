package rpc

// SESSIONS: attaching, listing, and the pid bindings a shell holds.
//
// One family per file: the surface is legible when a reader can see a whole
// family at once, and the May 2026 tightening drifted partly because 40
// method names and 70 types shared one 1,012-line file.

import ()

type FigaroInfoResponse struct {
	ID               string `json:"id"`
	State            string `json:"state"`
	Provider         string `json:"provider"`
	Model            string `json:"model"`
	MessageCount     int    `json:"message_count"`
	TokensIn         int    `json:"tokens_in"`
	TokensOut        int    `json:"tokens_out"`
	CacheReadTokens  int    `json:"cache_read_tokens"`       // cumulative cache-hit tokens
	CacheWriteTokens int    `json:"cache_write_tokens"`      // cumulative cache-write tokens
	ContextTokens    int    `json:"context_tokens"`          // estimated next-turn input size
	ContextLimit     int    `json:"context_limit,omitempty"` // effective prompt cap when known
	ContextExact     bool   `json:"context_exact"`           // true if from Usage watermark
	CreatedAt        int64  `json:"created_at"`              // unix millis
	LastActive       int64  `json:"last_active"`             // unix millis
	Mantra           string `json:"mantra"`                  // agent-maintained essence phrase (form "mantra")
	Cwd              string `json:"cwd"`                     // working directory (form "system.cwd")
	OutfitName       string `json:"outfit_name,omitempty"`   // form system.outfit_name
	OutfitVer        string `json:"outfit_ver,omitempty"`    // "live" if the stamped hash matches the current outfit, else its short hash
	BoundPIDs        []int  `json:"bound_pids"`

	// Fork-tree position (conversation nodes). Vector is the
	// child-index path (0, 0.0, 0.1, …); Trunk is the thread id that
	// flows down the continuation line; Parent is the aria branched from.
	// There is no "frozen" here: a fork leaves its target live under the
	// same id, so no aria is ever a read-only index node.
	Vector     []int  `json:"vector,omitempty"`
	Trunk      string `json:"trunk,omitempty"`
	Parent     string `json:"parent,omitempty"`
	BranchedLT uint64 `json:"branched_lt,omitempty"` // main-LT this trunk diverged at
	// Present is where the row is DRAWN: Parent unless a promote moved it.
	// Vector follows Present, so a listing draws one tree; Parent stays the
	// fork answer that `status` prints.
	Present string `json:"present,omitempty"`
	Kind    string `json:"kind,omitempty"` // "conversation" | "form" | "outfit" | "null" (set in global listings)

	// Node names the PEER this row came from, empty for local. A federated
	// listing without it is a pile of ids the reader cannot act on.
	Node string `json:"node,omitempty"`

	// Unbound-form rows only: the form's "name" key, and: when the form is
	// a role (duck-typed by the key's presence): its target-aria.
	Name       string `json:"name,omitempty"`
	TargetAria string `json:"target_aria,omitempty"`
}

// Endpoint describes how to connect to a figaro.
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

// ListRequest options. IDsOnly skips the per-aria form + tree fills
// (mantra, cwd, outfit hash, vector): much cheaper when the caller only needs
// the ids (e.g. shell completion). Global also includes the ceremonial anchors
// (the null genesis trunk + every versioned outfit) with Kind/Parent set, for
// the `ls -g` hierarchy and the `--json` escape hatch.
type ListRequest struct {
	IDsOnly bool `json:"ids_only,omitempty"`
	Global  bool `json:"global,omitempty"`
}

type ListResponse struct {
	Figaros []FigaroInfoResponse `json:"figaros"`
	// PeerErrors maps a peer name to why it could not be asked. It is
	// REPORTED rather than swallowed: a listing that silently omits an
	// unreachable machine tells the reader those arias are gone, which is a
	// different and much worse claim than "I could not ask".
	PeerErrors map[string]string `json:"peer_errors,omitempty"`
}

// PeerSpec is one federated node as the wire carries it.
type PeerSpec struct {
	Name      string `json:"name"`
	URL       string `json:"url"`
	TokenFile string `json:"token_file,omitempty"`
}

// PeerRequest is add (Peer set), remove (Remove set), or list (neither).
type PeerRequest struct {
	Peer   *PeerSpec `json:"peer,omitempty"`
	Remove string    `json:"remove,omitempty"`
}

type PeerResponse struct {
	Peers []PeerSpec `json:"peers"`
	// Reachable maps a peer name to its build string, or to the error that
	// stopped us. Filled only when the request asked for a probe.
	Reachable map[string]string `json:"reachable,omitempty"`
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
