package daemon

import (
	"github.com/jack-work/figaro/internal/authz"
	"github.com/jack-work/figaro/internal/sandbox/local"
	"github.com/jack-work/figaro/internal/toolcgroup"
)

func attributeTools(host *local.Host, peer *authz.Peercred) *toolcgroup.Registry {
	reg := toolcgroup.NewRegistry()
	peer.AttributeTools(reg)
	host.Tools = toolcgroup.Own(reg)
	return reg
}
