package daemon

import (
	"context"
	"crypto/ed25519"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"slices"
	"time"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/angelus"
	"github.com/jack-work/figaro/internal/authz"
	"github.com/jack-work/figaro/internal/boot"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/peer"
	"github.com/jack-work/figaro/internal/troupe"
)

func federate(ctx context.Context, a *angelus.Angelus, loaded *config.Loaded) error {
	return federateIn(ctx, a, loaded, boot.StateDir())
}

func federateIn(ctx context.Context, a *angelus.Angelus, loaded *config.Loaded, stateDir string) error {
	provider, err := loaded.TroupeProvider()
	if err != nil {
		return err
	}
	if provider == config.ProviderFigaro {
		return federateByLedger(ctx, a, loaded, stateDir)
	}
	creds, err := troupeKeys(ctx, a, loaded, stateDir)
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

func federateByLedger(ctx context.Context, a *angelus.Angelus, loaded *config.Loaded, stateDir string) error {
	door, ok, err := loaded.TLSDoor()
	switch {
	case err != nil:
		return err
	case !ok:
		return errors.New("troupe.provider figaro needs a [door.tls] for its peers to reach")
	case door.Cert != "":
		return errors.New("troupe.provider figaro serves its door on troupe keys: drop door.tls cert, key and client_ca")
	case door.Advertise == "":
		return fmt.Errorf("door.tls: listen %q is a wildcard, so set advertise to the host:port peers dial", door.Listen)
	}
	creds, err := troupeKeys(ctx, a, loaded, stateDir)
	if err != nil {
		return err
	}
	a.PeerVersions = &peer.Versions{}
	k := a.Troupe
	root, err := k.Dir.Root()
	if err != nil {
		slog.Error("troupe: no root certificate, so no members; the node door refuses peers", "troupe", k.Troupe, "err", err)
		a.Peers, _ = peer.NewStatic(0, nil, nil)
		return nil
	}
	store := newLedgerForm(a.Backend, k.Dir, k.Troupe)
	stored, err := store.Load()
	if err != nil {
		return err
	}
	l := troupe.NewLedger(k.Troupe, root)
	if _, err := l.Merge(stored...); err != nil {
		slog.Warn("troupe ledger: stored entries refused", "err", err)
	}
	m := peer.NewLedger(a.Backend.NodeID(), l, creds)
	m.Persist = store.Save
	if joined, err := k.Dir.Joined(); err == nil {
		if _, err := m.Merge(joined...); err != nil {
			slog.Warn("troupe ledger: entries from the join refused", "err", err)
		}
		if err := k.Dir.DropJoined(); err != nil {
			slog.Warn("troupe ledger", "err", err)
		}
	}
	a.Peers = m
	seedJoin(k, m, door.Advertise, time.Now())
	dialer := &peer.Dialer{Members: m, Versions: a.PeerVersions}
	m.OnEvict(dialer.Evict)
	k.Renew = renewer(ctx, m, dialer, door.Advertise)
	if k.Status().Err != nil {
		go func() {
			if err := k.Ensure(); err != nil {
				slog.Error("troupe certificate", "err", err)
			}
		}()
	}
	slog.Info("troupe ledger loaded", "troupe", k.Troupe, "entries", len(m.Entries()), "members", m.Nodes(), "version", m.Version())
	return nil
}

func renewer(ctx context.Context, m *peer.Ledger, d *peer.Dialer, door string) troupe.Renew {
	return func(pub ed25519.PublicKey) (*x509.Certificate, *x509.Certificate, error) {
		signers := slices.DeleteFunc(m.View().Signers(time.Now()), func(n string) bool { return n == m.Self() })
		if len(signers) == 0 {
			return nil, nil, errors.New("no member that holds an intermediate is known")
		}
		var errs []error
		for _, node := range signers {
			leaf, issuer, err := renewAt(ctx, d, node, rpc.TroupeSignRequest{Troupe: m.Troupe(), Node: m.Self(), PublicKey: pub, Door: door})
			if err == nil {
				return leaf, issuer, nil
			}
			errs = append(errs, fmt.Errorf("%s: %w", node, err))
		}
		return nil, nil, errors.Join(errs...)
	}
}

func renewAt(ctx context.Context, d *peer.Dialer, node string, req rpc.TroupeSignRequest) (*x509.Certificate, *x509.Certificate, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	l, err := d.Open(ctx, node, rpc.Passage{}, nil)
	if err != nil {
		return nil, nil, err
	}
	defer l.Close()
	resp, err := l.TroupeSign(ctx, req)
	if err != nil {
		return nil, nil, err
	}
	leaf, issuer, _, err := troupe.ParseSigned(resp.Chain, resp.Root)
	return leaf, issuer, err
}

func seedJoin(k *troupe.Keeper, m *peer.Ledger, door string, now time.Time) {
	inter, key, err := k.Dir.Intermediate()
	if err != nil || !now.Before(inter.NotAfter) {
		return
	}
	v, self := m.View(), m.Self()
	if gone := v.Gone[self]; gone != "" {
		slog.Warn("troupe: this node is out of the troupe and does not sign itself back in", "troupe", m.Troupe(), "by", gone)
		return
	}
	if mem, ok := v.Members[self]; ok && mem.Door == door && mem.SignsUntil.Equal(inter.NotAfter) {
		return
	}
	e, err := troupe.SignJoin(key, inter, m.Troupe(), self, door, now)
	if err == nil {
		_, err = m.Merge(e)
	}
	if err != nil {
		slog.Error("troupe: this node could not sign its own join", "err", err)
	}
}

func troupeKeys(ctx context.Context, a *angelus.Angelus, loaded *config.Loaded, stateDir string) (*peer.Credentials, error) {
	name, err := loaded.TroupeName()
	if err != nil || name == "" {
		return nil, err
	}
	k := &troupe.Keeper{Dir: troupe.DirFor(stateDir, name), Troupe: name, Node: a.Backend.NodeID()}
	if err := k.Ensure(); err != nil {
		slog.Error("troupe certificate: local callers are served, the node door refuses peers", "err", err)
	}
	go k.Run(ctx)
	a.Troupe = k
	return troupeCredentials(a), nil
}

func troupeCredentials(a *angelus.Angelus) *peer.Credentials {
	if m, ok := a.Peers.(*peer.Ledger); ok {
		return m.Credentials()
	}
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
	if m, ok := a.Peers.(*peer.Ledger); ok {
		m.OnEvict(nd.Evict)
		gossip(ctx, a, m, door.Advertise)
	}
	go func() {
		if err := a.Serve(ctx, tls.NewListener(ln, cfg), nd.Admit); err != nil {
			slog.Error("node door", "err", err)
		}
	}()
	return nil
}

func gossip(ctx context.Context, a *angelus.Angelus, m *peer.Ledger, door string) *peer.Gossip {
	g := &peer.Gossip{
		Members: m,
		Dialer:  &peer.Dialer{Members: m, Versions: a.PeerVersions},
		Seed:    func() { seedJoin(a.Troupe, m, door, time.Now()) },
	}
	m.OnEvict(g.Dialer.Evict)
	m.OnAdmit(g.Greet(ctx))
	a.PeerVersions.OnDrift(g.Drift(ctx))
	go g.Run(ctx)
	return g
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
