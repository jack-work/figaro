package troupe

import (
	"crypto"
	"crypto/ed25519"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/apikey"
)

const (
	APIKey       Kind = "apikey"
	APIKeyRevoke Kind = "apikey-revoke"

	MaxAPIKeys = 1024

	apiKeyDomain       = "figaro troupe apikey v1\n"
	apiKeyRevokeDomain = "figaro troupe apikey-revoke v1\n"

	PruneGrace = 24 * time.Hour
)

func isKeyKind(k Kind) bool { return k == APIKey || k == APIKeyRevoke }

func (e Entry) apiKeyMessage() []byte {
	if e.Kind == APIKeyRevoke {
		body, _ := json.Marshal(struct {
			Troupe string `json:"troupe"`
			Node   string `json:"node"`
			Key    string `json:"key"`
			By     string `json:"by"`
			At     int64  `json:"at"`
		}{e.Troupe, e.Node, e.Key, e.By, e.At})
		return append([]byte(apiKeyRevokeDomain), body...)
	}
	body, _ := json.Marshal(struct {
		Troupe    string   `json:"troupe"`
		Node      string   `json:"node"`
		Key       string   `json:"key"`
		Principal string   `json:"principal"`
		Hash      []byte   `json:"hash"`
		NotAfter  int64    `json:"not_after"`
		Nodes     []string `json:"nodes"`
		By        string   `json:"by"`
		At        int64    `json:"at"`
	}{e.Troupe, e.Node, e.Key, e.Principal, e.Hash, e.NotAfter, e.Nodes, e.By, e.At})
	return append([]byte(apiKeyDomain), body...)
}

func SignAPIKey(key crypto.Signer, inter *x509.Certificate, troupe, id, principal string, hash []byte, notAfter time.Time, nodes []string, by string, at time.Time) (Entry, error) {
	return sign(Entry{Kind: APIKey, Troupe: troupe, Node: IntermediateNode(inter), Key: id, Principal: principal, Hash: hash, NotAfter: notAfter.UnixMilli(), Nodes: nodes, By: by, At: at.UnixMilli(), Chain: [][]byte{inter.Raw}}, key)
}

func SignRootAPIKey(root ed25519.PrivateKey, troupe, id, principal string, hash []byte, notAfter time.Time, nodes []string, by string, at time.Time) (Entry, error) {
	return sign(Entry{Kind: APIKey, Troupe: troupe, Key: id, Principal: principal, Hash: hash, NotAfter: notAfter.UnixMilli(), Nodes: nodes, By: by, At: at.UnixMilli()}, root)
}

func CheckAudience(nodes []string) error {
	if len(nodes) == 0 || len(nodes) > apikey.MaxAudience {
		return fmt.Errorf("an api key names 1 to %d nodes, or %q for all", apikey.MaxAudience, apikey.AllNodes)
	}
	if len(nodes) == 1 && nodes[0] == apikey.AllNodes {
		return nil
	}
	for i, n := range nodes {
		if err := rpc.ValidateAriaID(n); err != nil {
			return fmt.Errorf("api key node %q: %w", n, err)
		}
		if i > 0 && nodes[i-1] >= n {
			return errors.New("an api key's nodes are sorted and named once")
		}
	}
	return nil
}

func SignAPIKeyRevoke(key crypto.Signer, leaf, inter *x509.Certificate, troupe, node, id, by string, at time.Time) (Entry, error) {
	return sign(Entry{Kind: APIKeyRevoke, Troupe: troupe, Node: node, Key: id, By: by, At: at.UnixMilli(), Chain: [][]byte{leaf.Raw, inter.Raw}}, key)
}

func verifyAPIKey(v verified, root *x509.Certificate, at time.Time) (crypto.PublicKey, error) {
	e := v.Entry
	if e.Door != "" || e.Owner != "" || e.Version != 0 || e.Body != "" {
		return nil, fmt.Errorf("a %s has no door, owner, version or body", e.Kind)
	}
	if err := apikey.ValidID(e.Key); err != nil {
		return nil, err
	}
	if err := ValidOwner(e.By); err != nil {
		return nil, fmt.Errorf("by: %w", err)
	}
	if e.Kind == APIKeyRevoke {
		if e.Principal != "" || e.Hash != nil || e.NotAfter != 0 || e.Nodes != nil {
			return nil, errors.New("an apikey-revoke names the key by id alone")
		}
		if len(v.chain) != 2 {
			return nil, errors.New("an apikey-revoke carries the serving node's certificate and its issuer")
		}
		leaf := v.chain[0]
		if err := chainsTo(leaf, v.chain[1], root, at); err != nil {
			return nil, err
		}
		if !names(leaf, Identity(e.Troupe, e.Node)) {
			return nil, fmt.Errorf("an apikey-revoke served by %s is signed by another node", e.Node)
		}
		return leaf.PublicKey, nil
	}
	if _, err := apikey.ParsePrincipal(e.Principal, e.Troupe); err != nil {
		return nil, err
	}
	if err := CheckAudience(e.Nodes); err != nil {
		return nil, err
	}
	if len(e.Hash) != apikey.HashSize {
		return nil, fmt.Errorf("an apikey carries a %d-byte hash", apikey.HashSize)
	}
	if e.NotAfter <= e.At || time.Duration(e.NotAfter-e.At)*time.Millisecond > apikey.MaxTTL {
		return nil, fmt.Errorf("an apikey expires after it is minted and within %s", apikey.MaxTTL)
	}
	if len(v.chain) == 0 {
		if e.Node != "" {
			return nil, errors.New("an apikey the root signs names no node")
		}
		return root.PublicKey, nil
	}
	if len(v.chain) != 1 {
		return nil, errors.New("an apikey carries the signing intermediate alone, or nothing when the root signs it")
	}
	inter := v.chain[0]
	if !inter.IsCA || inter.MaxPathLen != 0 || IntermediateNode(inter) != e.Node {
		return nil, errors.New("an apikey is signed by the intermediate of the node it names")
	}
	if err := chainsTo(inter, nil, root, at); err != nil {
		return nil, err
	}
	return inter.PublicKey, nil
}

func (e Entry) Expires() time.Time { return time.UnixMilli(e.NotAfter) }

func Prunable(e Entry, now time.Time) bool {
	switch e.Kind {
	case APIKey:
		return now.After(e.Expires().Add(PruneGrace))
	case APIKeyRevoke:
		return now.After(e.Time().Add(apikey.MaxTTL + PruneGrace))
	}
	return false
}

type APIKeyRevocation struct {
	Node string
	By   string
	At   time.Time
}

type APIKeyRecord struct {
	ID        string
	Principal string
	Hash      []byte
	NotAfter  time.Time
	Nodes     []string
	By        string
	Signer    string
	At        time.Time
	Revoked   *APIKeyRevocation
	Void      string
}

func (r APIKeyRecord) Live(now time.Time) bool {
	return r.Revoked == nil && r.Void == "" && now.Before(r.NotAfter)
}

const RootSigner = "root"

func (l *Ledger) apiKeyView(ids []string, gone map[string]Kind, policy *Entry) map[string]APIKeyRecord {
	var setters []string
	if policy != nil {
		setters, _ = PolicySetters(policy.Body)
	}
	out := map[string]APIKeyRecord{}
	clash := map[string]bool{}
	for _, id := range ids {
		e := l.entries[id]
		if e.Kind != APIKey {
			continue
		}
		if _, seen := out[e.Key]; seen {
			clash[e.Key] = true
			continue
		}
		r := APIKeyRecord{ID: e.Key, Principal: e.Principal, Hash: e.Hash, NotAfter: e.Expires(), Nodes: e.Nodes, By: e.By, Signer: e.Node, At: e.Time()}
		switch {
		case e.RootSigned():
			r.Signer = RootSigner
		case gone[e.Node] == Revoke:
			r.Void = "its signer " + e.Node + " is revoked by the root"
		case policy == nil:
			r.Void = "no troupe policy is in force to name its signer " + e.Node + " a setter"
		case !slices.Contains(setters, e.Node):
			r.Void = fmt.Sprintf("its signer %s is not a setter in policy version %d", e.Node, policy.Version)
		}
		out[e.Key] = r
	}
	for key := range clash {
		r := out[key]
		r.Void = "two keys were minted under this id"
		out[key] = r
	}
	for _, id := range ids {
		e := l.entries[id]
		if e.Kind != APIKeyRevoke {
			continue
		}
		r, ok := out[e.Key]
		if !ok || r.Revoked != nil && !e.Time().Before(r.Revoked.At) {
			continue
		}
		r.Revoked = &APIKeyRevocation{Node: e.Node, By: e.By, At: e.Time()}
		out[e.Key] = r
	}
	return out
}

func (l *Ledger) Prune(now time.Time) []string {
	var out []string
	for id, e := range l.entries {
		if Prunable(e.Entry, now) {
			delete(l.entries, id)
			l.count(e.Kind, -1)
			out = append(out, id)
		}
	}
	if len(out) > 0 {
		l.rebuild()
	}
	return out
}
