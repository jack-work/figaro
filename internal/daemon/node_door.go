package daemon

import (
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/jack-work/figaro/internal/angelus"
	"github.com/jack-work/figaro/internal/authz"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/peer"
)

func federate(a *angelus.Angelus, loaded *config.Loaded) error {
	peers, err := loaded.Peers()
	if err != nil {
		return err
	}
	self := a.Backend.NodeID()
	for _, p := range peers {
		if p.Node == self {
			return fmt.Errorf("peers.%s is this node", p.Node)
		}
	}
	version, err := loaded.TroupeVersion()
	if err != nil {
		return err
	}
	members, err := peer.NewStatic(version, peers)
	if err != nil {
		return err
	}
	a.Peers = members
	a.PeerVersions = &peer.Versions{}
	return nil
}

func serveNodeDoor(ctx context.Context, a *angelus.Angelus, loaded *config.Loaded) error {
	door, ok, err := loaded.TLSDoor()
	if err != nil || !ok {
		return err
	}
	cfg, err := peer.ServerTLS(door)
	if err != nil {
		return err
	}
	verify, err := forwardedCallers(loaded.Config.Authz)
	if err != nil {
		return fmt.Errorf("door.tls: %w", err)
	}
	ln, err := net.Listen("tcp", door.Listen)
	if err != nil {
		return fmt.Errorf("door.tls: %w", err)
	}
	slog.Info("node door listening", "addr", ln.Addr().String(), "peers", a.Peers.Nodes(), "troupe_version", a.Peers.Version(), "forwarded_callers", verify != nil)
	nd := &peer.Door{Members: a.Peers, Versions: a.PeerVersions, Verify: verify}
	go func() {
		if err := a.Serve(ctx, tls.NewListener(ln, cfg), nd.Admit); err != nil {
			slog.Error("node door", "err", err)
		}
	}()
	return nil
}

func forwardedCallers(cfg config.AuthzConfig) (peer.Verify, error) {
	if cfg.Bearer == nil {
		slog.Warn("door.tls: no [authz.bearer], so every forwarded caller is refused")
		return nil, nil
	}
	b, err := newBearer(*cfg.Bearer)
	if err != nil {
		return nil, err
	}
	return func(ctx context.Context, token string) (context.Context, error) {
		s, err := b.Open(ctx, token)
		if err != nil {
			return nil, err
		}
		return authz.WithSession(ctx, s), nil
	}, nil
}

func newBearer(cfg config.BearerConfig) (*authz.Bearer, error) {
	return authz.NewBearer(cfg, &http.Client{Timeout: 5 * time.Second}, nil)
}
