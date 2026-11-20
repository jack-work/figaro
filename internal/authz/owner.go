package authz

import (
	"context"

	"github.com/jack-work/figaro/internal/authn"
	"github.com/jack-work/jkauthz"
)

func LocalOrOwner(owner jkauthz.Principal) Policy {
	return PolicyFunc(func(r Request) Decision {
		if PassesUnpoliced(r.Door, owner, r.Subject) {
			return Allow()
		}
		return Deny("%s: no policy is loaded, so the %s door passes only the owner", r.Method, r.Door)
	})
}

func PassesUnpoliced(d authn.Door, owner jkauthz.Principal, s jkauthz.Subject) bool {
	return d.Trusted() || IsOwner(owner, s)
}

func IsOwner(owner jkauthz.Principal, s jkauthz.Subject) bool {
	return !owner.IsZero() && s.Principal == owner
}

func OwnerCall(ctx context.Context, owner jkauthz.Principal, s jkauthz.Subject) bool {
	_, tool := authn.ToolOf(ctx)
	return !tool && IsOwner(owner, s)
}
