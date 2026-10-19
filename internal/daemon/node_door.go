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
	"github.com/jack-work/figaro/internal/boot"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/peer"
	"github.com/jack-work/figaro/internal/troupe"
)

func federate(ctx context.Context, a *angelus.Angelus, loaded *config.Loaded) error {
	creds, err := troupeKeys(ctx, a, loaded)
	if err != nil {
		return err
	}
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
	members, err := peer.NewStatic(version, peers, creds)
	if err != nil {
		return err
	}
	a.Peers = members
	a.PeerVersions = &peer.Versions{}
	return nil
}

func troupeKeys(ctx context.Context, a *angelus.Angelus, loaded *config.Loaded) (*peer.Credentials, error) {
	name, err := loaded.TroupeName()
	if err != nil || name == "" {
		return nil, err
	}
	k := &troupe.Keeper{Dir: troupe.DirFor(boot.StateDir(), name), Troupe: name, Node: a.Backend.NodeID()}
	if err := k.Ensure(); err != nil {
		slog.Error("troupe certificate: local callers are served, the node door refuses peers", "err", err)
	}
	go k.Run(ctx)
	a.Troupe = k
	return troupeCredentials(a), nil
}

func troupeCredentials(a *angelus.Angelus) *peer.Credentials {
	if a.Troupe == nil {
		return nil
	}
	return &peer.Credentials{Certificate: a.Troupe.Current, Roots: a.Troupe.Roots}
}

func serveNodeDoor(ctx context.Context, a *angelus.Angelus, loaded *config.Loaded) error {
	door, ok, err := loaded.TLSDoor()
	if err != nil || !ok {
		return err
	}
	cfg, err := peer.ServerTLS(door, troupeCredentials(a))
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
