package authz

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/troupe"
	"github.com/jack-work/jkauthz"
)

type TroupeDocument struct {
	Issuers []config.IssuerConfig
	policy  []byte
}

func ParseTroupeDocument(body []byte) (TroupeDocument, error) {
	var raw map[string]any
	if _, err := toml.Decode(string(body), &raw); err != nil {
		return TroupeDocument{}, err
	}
	var doc TroupeDocument
	if _, ok := raw["issuers"]; ok {
		var only struct {
			Issuers []config.IssuerConfig `toml:"issuers"`
		}
		if _, err := toml.Decode(string(body), &only); err != nil {
			return TroupeDocument{}, fmt.Errorf("issuers: %w", err)
		}
		doc.Issuers = only.Issuers
		delete(raw, "issuers")
	}
	if _, ok := raw["setters"]; ok {
		if _, err := troupe.PolicySetters(string(body)); err != nil {
			return TroupeDocument{}, err
		}
		delete(raw, "setters")
	}
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(raw); err != nil {
		return TroupeDocument{}, err
	}
	doc.policy = buf.Bytes()
	return doc, nil
}

func CompileTroupePolicy(body []byte, troupe string, schema jkauthz.Schema[Resource]) (*jkauthz.Policy[Resource], error) {
	doc, err := ParseTroupeDocument(body)
	if err != nil {
		return nil, fmt.Errorf("troupe policy: %w", err)
	}
	pol, err := jkauthz.Compile(doc.policy, schema)
	if err != nil {
		return nil, fmt.Errorf("troupe policy: %w", err)
	}
	if err := checkTroupeRules(doc, troupe); err != nil {
		return nil, err
	}
	return pol, nil
}

func CheckTroupeDocument(body []byte, troupe string, facts Facts) error {
	_, err := CompileTroupePolicy(body, troupe, Schema(facts))
	return err
}

func checkTroupeRules(doc TroupeDocument, troupe string) error {
	for _, is := range doc.Issuers {
		if !strings.HasPrefix(is.Issuer, "https://") {
			return fmt.Errorf("troupe policy: issuer %q: a troupe names only https issuers", is.Issuer)
		}
		if _, err := IssuerScope(is.Issuer); err != nil {
			return fmt.Errorf("troupe policy: %w", err)
		}
		if err := checkTroupeClients(is); err != nil {
			return fmt.Errorf("troupe policy: %w", err)
		}
	}
	var declared struct {
		Roles map[string]struct {
			Grants []string `toml:"grants"`
		} `toml:"roles"`
		Bind []struct {
			Principals []string `toml:"principals"`
		} `toml:"bind"`
	}
	if _, err := toml.Decode(string(doc.policy), &declared); err != nil {
		return err
	}
	for _, b := range declared.Bind {
		for _, name := range b.Principals {
			pr, err := jkauthz.ParsePrincipal(name)
			if err != nil {
				continue
			}
			if pr.Kind == KindTroupe {
				return fmt.Errorf("troupe policy: %s is the troupe itself, which a node's own policy names to delegate; the troupe cannot bind it", name)
			}
			if acrossIssuers(pr) {
				return fmt.Errorf("troupe policy: %s matches that kind from every issuer, troupe issuer and api key on every member; bind the principals by name", name)
			}
		}
	}
	for role, r := range declared.Roles {
		for _, text := range r.Grants {
			g, err := jkauthz.ParseGrant(text)
			if err != nil {
				return err
			}
			switch {
			case g.Object == selSandbox+"local":
				return fmt.Errorf("troupe policy: role %s grants %s, which only each node's own policy decides", role, g)
			case g.Object == selTroupe+troupe && (g.Verb == jkauthz.Any || g.Verb == string(rpc.VerbAdmin)):
				return fmt.Errorf("troupe policy: role %s grants %s, which only each signer node's own policy decides (join may be granted)", role, g)
			}
		}
	}
	return nil
}
