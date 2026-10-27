package authz

import (
	"context"
	"net"
	"strings"

	"github.com/jack-work/jkauthz"
)

const BoxScope = "local:box"

func BoxPrincipal(name string) jkauthz.Principal {
	return jkauthz.Principal{Kind: jkauthz.KindSandbox, ID: name + "@" + BoxScope}
}

func BoxOf(p jkauthz.Principal) (string, bool) {
	if p.Kind != jkauthz.KindSandbox || p.Node != "" {
		return "", false
	}
	name, ok := strings.CutSuffix(p.ID, "@"+BoxScope)
	if !ok || name == "" {
		return "", false
	}
	return name, true
}

func BoxAdmit(name string) func(context.Context, net.Conn) (context.Context, error) {
	s := jkauthz.Subject{Principal: BoxPrincipal(name)}
	return func(ctx context.Context, _ net.Conn) (context.Context, error) {
		return WithSubject(ctx, s), nil
	}
}
