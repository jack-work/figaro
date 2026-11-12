package authz

import "github.com/jack-work/jkauthz"

const PoolScope = "local:pool"

const provisionerID = "provisioner@" + PoolScope

func ProvisionerPrincipal() jkauthz.Principal {
	return jkauthz.Principal{Kind: KindService, ID: provisionerID}
}

func IsProvisioner(p jkauthz.Principal) bool {
	return p.Kind == KindService && p.ID == provisionerID && p.Node == ""
}
