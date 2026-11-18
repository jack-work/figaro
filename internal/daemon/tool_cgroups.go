package daemon

import (
	"log/slog"

	"github.com/jack-work/figaro/internal/authz"
	"github.com/jack-work/figaro/internal/sandbox/local"
	"github.com/jack-work/figaro/internal/toolcgroup"
)

func attributeTools(host *local.Host, peer *authz.Peercred) *toolcgroup.Registry {
	reg := toolcgroup.NewRegistry()
	peer.AttributeTools(reg)
	tree, err := toolcgroup.Own(reg)
	if err != nil {
		slog.Error("peer check: tools cannot start until this daemon may create cgroups; [authz] peer_check = false runs them unattributed", "err", err)
		host.Tools = unplaceable{err}
		return reg
	}
	slog.Info("peer check: each figaro's tools run in its own cgroup", "under", tree.Root())
	host.Tools = tree
	return reg
}

type unplaceable struct{ err error }

func (u unplaceable) FD(string) (int, error) { return -1, u.err }
