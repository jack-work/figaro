package authz

import (
	"fmt"
	"slices"
	"strings"

	"github.com/jack-work/jkauthz"

	"github.com/jack-work/figaro/internal/apikey"
)

func refuseWhatAPoolPolicyCannotBind(policy []byte, issuerScopes []string) error {
	troupeScope := apikey.Scope("")
	for _, b := range binds(policy) {
		for _, name := range b.Principals {
			pr, err := jkauthz.ParsePrincipal(name)
			if err != nil {
				return err
			}
			_, scope, _ := strings.Cut(pr.ID, "@")
			var why string
			switch {
			case pr.ID == jkauthz.Any:
				why = "it matches every principal of that kind"
			case pr.Node != "" || pr.Kind == jkauthz.KindNode:
				why = "it reaches in from another node"
			case pr.Kind == jkauthz.KindSandbox:
				why = "a box holds what its lease computes and nothing a file says"
			case strings.HasPrefix(scope, troupeScope):
				why = "it is a troupe's principal or an api key's"
			case scope != "" && !slices.Contains(issuerScopes, scope):
				why = "no issuer its config names (" + orNone(issuerScopes) + ") can vouch for it"
			default:
				continue
			}
			return fmt.Errorf("a daemon that leases slots binds %s: %s; its policy names broker's client and the admin, each by name", name, why)
		}
		for _, g := range b.Groups {
			_, scope, _ := strings.Cut(g, "@")
			if strings.HasPrefix(scope, troupeScope) {
				return fmt.Errorf("a daemon that leases slots binds group %s: it is a troupe's group", g)
			}
			if !slices.Contains(issuerScopes, scope) {
				return fmt.Errorf("a daemon that leases slots binds group %s: a group comes from an issuer its config names (%s), and this one does not", g, orNone(issuerScopes))
			}
		}
	}
	return nil
}

func orNone(names []string) string {
	if len(names) == 0 {
		return "it names none"
	}
	return strings.Join(names, ", ")
}
