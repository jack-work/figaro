package daemon

import (
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"
	"net"

	"github.com/jack-work/figaro/internal/angelus"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/peer"
)

func serveNodeDoor(ctx context.Context, a *angelus.Angelus, loaded *config.Loaded) error {
	door, ok, err := loaded.TLSDoor()
	if err != nil || !ok {
		return err
	}
	peers, err := loaded.Peers()
	if err != nil {
		return err
	}
	members, err := peer.NewMembers(peers)
	if err != nil {
		return err
	}
	cfg, err := peer.ServerTLS(door)
	if err != nil {
		return err
	}
	ln, err := net.Listen("tcp", door.Listen)
	if err != nil {
		return fmt.Errorf("door.tls: %w", err)
	}
	slog.Info("node door listening", "addr", ln.Addr().String(), "peers", members.Nodes())
	nd := &peer.Door{Members: members}
	go func() {
		if err := a.Serve(ctx, tls.NewListener(ln, cfg), nd.Admit); err != nil {
			slog.Error("node door", "err", err)
		}
	}()
	return nil
}
