package troupe

import (
	"bytes"
	"crypto"
	"crypto/sha256"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

const (
	policyDomain  = "figaro troupe policy v1\n"
	MaxPolicyBody = 64 << 10
)

func (e Entry) policyMessage() []byte {
	body, _ := json.Marshal(struct {
		Troupe  string `json:"troupe"`
		Node    string `json:"node"`
		Version uint64 `json:"version"`
		By      string `json:"by"`
		Body    string `json:"body"`
		At      int64  `json:"at"`
	}{e.Troupe, e.Node, e.Version, e.By, e.Body, e.At})
	return append([]byte(policyDomain), body...)
}

func SignPolicy(key crypto.Signer, inter *x509.Certificate, troupe string, version uint64, by, body string, at time.Time) (Entry, error) {
	return sign(Entry{Kind: Policy, Troupe: troupe, Node: IntermediateNode(inter), Version: version, By: by, Body: body, At: at.UnixMilli(), Chain: [][]byte{inter.Raw}}, key)
}

func verifyPolicy(v verified, root *x509.Certificate, at time.Time) (*x509.Certificate, error) {
	e := v.Entry
	if e.Door != "" || e.Owner != "" {
		return nil, errors.New("a policy has no door or owner")
	}
	if e.Version == 0 {
		return nil, errors.New("a policy starts at version 1")
	}
	if e.Body == "" || len(e.Body) > MaxPolicyBody {
		return nil, fmt.Errorf("a policy body holds 1 to %d bytes", MaxPolicyBody)
	}
	if err := ValidOwner(e.By); err != nil {
		return nil, fmt.Errorf("by: %w", err)
	}
	if len(v.chain) != 1 {
		return nil, errors.New("a policy carries the signing intermediate alone")
	}
	inter := v.chain[0]
	if !inter.IsCA || inter.MaxPathLen != 0 || IntermediateNode(inter) != e.Node {
		return nil, errors.New("a policy is signed by the intermediate of the node it names")
	}
	if err := chainsTo(inter, nil, root, at); err != nil {
		return nil, err
	}
	return inter, nil
}

func Newer(a, b Entry) bool {
	if a.Version != b.Version {
		return a.Version > b.Version
	}
	ha, hb := sha256.Sum256([]byte(a.Body)), sha256.Sum256([]byte(b.Body))
	if c := bytes.Compare(ha[:], hb[:]); c != 0 {
		return c > 0
	}
	return a.At > b.At
}
