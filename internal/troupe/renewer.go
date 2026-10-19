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
)

const renewerDomain = "figaro troupe renewer v1\n"

func (e Entry) renewerMessage() []byte {
	body, _ := json.Marshal(struct {
		Troupe string `json:"troupe"`
		Node   string `json:"node"`
		Renews string `json:"renews"`
		At     int64  `json:"at"`
	}{e.Troupe, e.Node, e.Renews, e.At})
	return append([]byte(renewerDomain), body...)
}

func SignRootRenewer(root ed25519.PrivateKey, troupe, node, renewer string, at time.Time) (Entry, error) {
	return sign(Entry{Kind: Renewer, Troupe: troupe, Node: node, Renews: renewer, At: at.UnixMilli()}, root)
}

func SignOwnRenewer(key crypto.Signer, inter *x509.Certificate, troupe, renewer string, at time.Time) (Entry, error) {
	return sign(Entry{Kind: Renewer, Troupe: troupe, Node: IntermediateNode(inter), Renews: renewer, At: at.UnixMilli(), Chain: [][]byte{inter.Raw}}, key)
}

func verifyRenewer(v verified, troupe string, root *x509.Certificate, at time.Time) (crypto.PublicKey, error) {
	e := v.Entry
	if e.Door != "" || e.Owner != "" {
		return nil, errors.New("a renewer entry has no door or owner")
	}
	if err := rpc.ValidateAriaID(e.Renews); err != nil {
		return nil, fmt.Errorf("renews: %w", err)
	}
	if e.Renews == e.Node {
		return nil, errors.New("a node's own intermediate renews it without an entry")
	}
	switch len(v.chain) {
	case 0:
		return root.PublicKey, nil
	case 1:
		inter := v.chain[0]
		if !inter.IsCA || inter.MaxPathLen != 0 || IntermediateNode(inter) != e.Node {
			return nil, errors.New("a renewer entry is signed by the root or by the intermediate of the node it is for")
		}
		if err := chainsTo(inter, nil, root, at); err != nil {
			return nil, err
		}
		return inter.PublicKey, nil
	}
	return nil, errors.New("a renewer entry carries its node's intermediate alone, or nothing when the root signs it")
}

func (v View) MayIssue(node, issuer string) bool {
	return issuer == node || slices.Contains(v.Renewers[node], issuer)
}
