package authn

import "github.com/jack-work/jkauthz"

const (
	KindTroupe  jkauthz.Kind = "troupe"
	KindService jkauthz.Kind = "service"
)

func TroupePrincipal(name string) jkauthz.Principal {
	return jkauthz.Principal{Kind: KindTroupe, ID: name}
}

const PoolScope = "local:pool"

const provisionerID = "provisioner@" + PoolScope

func ProvisionerPrincipal() jkauthz.Principal {
	return jkauthz.Principal{Kind: KindService, ID: provisionerID}
}

func IsProvisioner(p jkauthz.Principal) bool {
	return p.Kind == KindService && p.ID == provisionerID && p.Node == ""
}

func ProvisionerServing(box string) jkauthz.Subject {
	return jkauthz.Subject{Principal: ProvisionerPrincipal(), Groups: []string{box + "@" + PoolScope}}
}
