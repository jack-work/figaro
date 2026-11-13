package authz

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/jack-work/jkauthz"

	"github.com/jack-work/figaro/api/rpc"
)

const PoolScope = "local:pool"

const provisionerID = "provisioner@" + PoolScope

func ProvisionerPrincipal() jkauthz.Principal {
	return jkauthz.Principal{Kind: KindService, ID: provisionerID}
}

func IsProvisioner(p jkauthz.Principal) bool {
	return p.Kind == KindService && p.ID == provisionerID && p.Node == ""
}

type Leases interface {
	InForce(box string) bool
}

func (p *Policies) SetLeases(l Leases) {
	p.leases.Store(&l)
}

func ProvisionerServing(box string) jkauthz.Subject {
	return jkauthz.Subject{Principal: ProvisionerPrincipal(), Groups: []string{box + "@" + PoolScope}}
}

func leaseServed(s jkauthz.Subject) (string, bool) {
	if len(s.Groups) != 1 {
		return "", false
	}
	box, ok := strings.CutSuffix(s.Groups[0], "@"+PoolScope)
	return box, ok && box != ""
}

func provisionerGrants(box string) jkauthz.Grants {
	return jkauthz.Grants{
		{Verb: string(rpc.VerbUse), Object: selSandbox + box},
		{Verb: string(rpc.VerbCreate), Object: selSandbox + box},
		{Verb: string(rpc.VerbSet), Object: selBoxedIn + box},
		{Verb: string(rpc.VerbKill), Object: selBoxedIn + box},
	}
}

var leaseName = regexp.MustCompile(`-g[0-9]+$`)

func (p *Policies) leasedBox(principal jkauthz.Principal) (string, bool) {
	if !p.pool.Load() {
		return "", false
	}
	box, ok := BoxOf(principal)
	if !ok || !leaseName.MatchString(box) {
		return "", false
	}
	return box, true
}

func leasedGrants() jkauthz.Grants {
	return jkauthz.Grants{{Verb: string(rpc.VerbSay), Object: selLease}}
}

func (p *Policies) allowLeased(s jkauthz.Subject, box string, verb rpc.Verb, r Resource) Verdict {
	leases := p.leases.Load()
	if leases == nil || !(*leases).InForce(box) {
		return Verdict{computed: fmt.Sprintf("%s holds nothing: lease %s is not in force", s.Principal, box)}
	}
	m, _ := p.schema.Selectors.(selectors)
	for _, g := range leasedGrants() {
		if g.Verb == string(verb) && m.Match(g.Object, s, r) {
			return Verdict{
				Decision: jkauthz.Decision{Allowed: true, Role: "lease", Selector: g.Object},
				computed: fmt.Sprintf("%s holds lease %s: %s on %s", s.Principal, box, verb, g.Object),
			}
		}
	}
	return Verdict{computed: fmt.Sprintf("%s may not %s on %s: a lease's grant is %s", s.Principal, verb, r, leasedGrants())}
}

func (p *Policies) allowProvisioner(s jkauthz.Subject, verb rpc.Verb, r Resource) Verdict {
	box, ok := leaseServed(s)
	if !ok {
		return Verdict{computed: fmt.Sprintf("%s holds nothing: it serves one lease at a time, and this call names %d", s.Principal, len(s.Groups))}
	}
	leases := p.leases.Load()
	if leases == nil || !(*leases).InForce(box) {
		return Verdict{computed: fmt.Sprintf("%s holds nothing: lease %s is not minting, live or releasing", s.Principal, box)}
	}
	m, _ := p.schema.Selectors.(selectors)
	grants := provisionerGrants(box)
	for _, g := range grants {
		if g.Verb == string(verb) && m.Match(g.Object, s, r) {
			return Verdict{
				Decision: jkauthz.Decision{Allowed: true, Role: "provisioner", Selector: g.Object},
				computed: fmt.Sprintf("%s serves lease %s: %s on %s", s.Principal, box, verb, g.Object),
			}
		}
	}
	return Verdict{computed: fmt.Sprintf("%s serving lease %s may not %s on %s: its grant is %s", s.Principal, box, verb, r, grants)}
}
