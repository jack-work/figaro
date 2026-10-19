package troupe

import (
	"bytes"
	"crypto"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/jack-work/figaro/api/rpc"
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

func SignRootPolicy(root ed25519.PrivateKey, troupe string, version uint64, by, body string, at time.Time) (Entry, error) {
	return sign(Entry{Kind: Policy, Troupe: troupe, Version: version, By: by, Body: body, At: at.UnixMilli()}, root)
}

func (e Entry) RootSigned() bool {
	return (e.Kind == Policy || e.Kind == APIKey) && len(e.Chain) == 0
}

func PolicySetters(body string) ([]string, error) {
	var doc struct {
		Setters []string `toml:"setters"`
	}
	if _, err := toml.Decode(body, &doc); err != nil {
		return nil, fmt.Errorf("setters: %w", err)
	}
	for _, n := range doc.Setters {
		if err := rpc.ValidateAriaID(n); err != nil {
			return nil, fmt.Errorf("setters: %w", err)
		}
	}
	return doc.Setters, nil
}

func verifyPolicy(v verified, root *x509.Certificate, at time.Time) (crypto.PublicKey, error) {
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
	if _, err := PolicySetters(e.Body); err != nil {
		return nil, err
	}
	if len(v.chain) == 0 {
		if e.Node != "" {
			return nil, errors.New("a policy the root signs names no node")
		}
		return root.PublicKey, nil
	}
	if len(v.chain) != 1 {
		return nil, errors.New("a policy carries the signing intermediate alone, or nothing when the root signs it")
	}
	inter := v.chain[0]
	if !inter.IsCA || inter.MaxPathLen != 0 || IntermediateNode(inter) != e.Node {
		return nil, errors.New("a policy is signed by the intermediate of the node it names")
	}
	if err := chainsTo(inter, nil, root, at); err != nil {
		return nil, err
	}
	return inter.PublicKey, nil
}

type policyWalk struct {
	adopted *Entry
	rivals  []Entry
	refused []Entry
}

func walkPolicies(es []Entry) policyWalk {
	slices.SortFunc(es, func(a, b Entry) int {
		switch {
		case a.Version != b.Version:
			return cmpU(a.Version, b.Version)
		case Newer(a, b):
			return 1
		case Newer(b, a):
			return -1
		}
		return 0
	})
	var w policyWalk
	var setters []string
	for i := 0; i < len(es); {
		j := i
		for j < len(es) && es[j].Version == es[i].Version {
			j++
		}
		var ok []Entry
		for _, e := range es[i:j] {
			if e.RootSigned() || w.adopted != nil && slices.Contains(setters, e.Node) {
				ok = append(ok, e)
			} else {
				w.refused = append(w.refused, e)
			}
		}
		if len(ok) > 0 {
			best := ok[len(ok)-1]
			w.adopted = &best
			setters, _ = PolicySetters(best.Body)
			w.rivals = nil
			for _, e := range ok {
				if e.Body != best.Body {
					w.rivals = append(w.rivals, e)
				}
			}
		}
		i = j
	}
	return w
}

func cmpU(a, b uint64) int {
	if a < b {
		return -1
	}
	return 1
}

func Newer(a, b Entry) bool {
	if a.Version != b.Version {
		return a.Version > b.Version
	}
	if a.RootSigned() != b.RootSigned() {
		return a.RootSigned()
	}
	ha, hb := sha256.Sum256([]byte(a.Body)), sha256.Sum256([]byte(b.Body))
	if c := bytes.Compare(ha[:], hb[:]); c != 0 {
		return c > 0
	}
	return a.At > b.At
}
