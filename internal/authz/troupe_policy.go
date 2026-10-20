package authz

import (
	"bytes"
	"fmt"
	"slices"
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

func CompileTroupePolicy(body []byte, schema jkauthz.Schema[Resource]) (*jkauthz.Policy[Resource], error) {
	doc, err := ParseTroupeDocument(body)
	if err != nil {
		return nil, fmt.Errorf("troupe policy: %w", err)
	}
	pol, err := jkauthz.Compile(doc.policy, schema)
	if err != nil {
		return nil, fmt.Errorf("troupe policy: %w", err)
	}
	return pol, nil
}

func CheckTroupeDocument(body []byte, troupe string, facts Facts) error {
	if _, err := CompileTroupePolicy(body, Schema(facts)); err != nil {
		return err
	}
	doc, _ := ParseTroupeDocument(body)
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
	var grants struct {
		Roles map[string]struct {
			Grants []struct {
				Verbs []string `toml:"verbs"`
				On    []string `toml:"on"`
			} `toml:"grants"`
		} `toml:"roles"`
		Bind []struct {
			Principals []string `toml:"principals"`
		} `toml:"bind"`
	}
	if _, err := toml.Decode(string(doc.policy), &grants); err != nil {
		return err
	}
	for _, b := range grants.Bind {
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
	for role, r := range grants.Roles {
		for _, g := range r.Grants {
			all := slices.Contains(g.Verbs, jkauthz.Any)
			for _, on := range g.On {
				switch {
				case on == selSandbox+"local":
					return fmt.Errorf("troupe policy: role %s grants on sandbox:local, which only each node's own policy decides", role)
				case on == selTroupe+troupe && (all || slices.Contains(g.Verbs, string(rpc.VerbAdmin))):
					return fmt.Errorf("troupe policy: role %s grants admin on troupe:%s, which only each signer node's own policy decides (join may be granted)", role, troupe)
				}
			}
		}
	}
	return nil
}
