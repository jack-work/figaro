package rpc

const (
	MethodTroupeAPIKeyMint   = "troupe.apikey.mint"
	MethodTroupeAPIKeyRevoke = "troupe.apikey.revoke"
	MethodTroupeAPIKeyList   = "troupe.apikey.ls"
)

type TroupeAPIKeyMintRequest struct {
	Troupe    string   `json:"troupe"`
	ID        string   `json:"id"`
	Principal string   `json:"principal"`
	Hash      []byte   `json:"hash"`
	TTLMillis int64    `json:"ttl_ms,omitempty"`
	Nodes     []string `json:"nodes,omitempty"`
}

type TroupeAPIKeyMintResponse struct {
	Key         TroupeAPIKey      `json:"key"`
	Reached     []string          `json:"reached,omitempty"`
	Unreachable []NodeUnreachable `json:"unreachable,omitempty"`
}

type TroupeAPIKeyRevokeRequest struct {
	Troupe string `json:"troupe"`
	ID     string `json:"id"`
}

type TroupeAPIKeyRevokeResponse struct {
	Key         TroupeAPIKey      `json:"key"`
	Already     bool              `json:"already,omitempty"`
	Reached     []string          `json:"reached,omitempty"`
	Unreachable []NodeUnreachable `json:"unreachable,omitempty"`
}

type TroupeAPIKeyListRequest struct {
	Troupe string `json:"troupe"`
}

type TroupeAPIKey struct {
	ID         string   `json:"id"`
	Principal  string   `json:"principal"`
	By         string   `json:"by"`
	Signer     string   `json:"signer"`
	MintedAt   int64    `json:"minted_at"`
	NotAfter   int64    `json:"not_after"`
	Nodes      []string `json:"nodes"`
	RevokedBy  string   `json:"revoked_by,omitempty"`
	RevokedVia string   `json:"revoked_via,omitempty"`
	RevokedAt  int64    `json:"revoked_at,omitempty"`
	Void       string   `json:"void,omitempty"`
}

type TroupeAPIKeyListResponse struct {
	Troupe string         `json:"troupe"`
	Caller string         `json:"caller"`
	All    bool           `json:"all"`
	Keys   []TroupeAPIKey `json:"keys"`
}
