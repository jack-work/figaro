package authz

import (
	"fmt"
	"strings"

	"github.com/jack-work/jkauthz"

	"github.com/jack-work/figaro/internal/apikey"
)

func refuseWhatAPoolPolicyCannotBind(policy []byte) error {
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
			default:
				continue
			}
			return fmt.Errorf("a daemon that leases slots binds %s: %s; its policy names broker's client and the admin, each by name", name, why)
		}
		for _, g := range b.Groups {
			if _, scope, _ := strings.Cut(g, "@"); strings.HasPrefix(scope, troupeScope) {
				return fmt.Errorf("a daemon that leases slots binds group %s: it is a troupe's group", g)
			}
		}
	}
	return nil
}
