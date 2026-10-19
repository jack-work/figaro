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
	"os"
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
	return bindStaticTroupePolicy(a, loaded)
}

func bindStaticTroupePolicy(a *angelus.Angelus, loaded *config.Loaded) error {
	if a.Policies == nil {
		return nil
	}
	name, err := loaded.TroupeName()
	if err != nil {
		return err
	}
	path, err := loaded.StaticTroupePolicy()
	if err != nil {
		return err
	}
	var body []byte
	if path != "" {
		if body, err = os.ReadFile(path); err != nil {
			return fmt.Errorf("troupe.policy: %w", err)
		}
	}
	if err := a.ApplyTroupeDocument(name, body, loaded.Config.Authz.TakesTroupeIssuers()); err != nil {
		slog.Error("troupe policy: it grants nothing until it loads", "path", path, "err", err)
	}
	return nil
}

func bindLedgerTroupePolicy(a *angelus.Angelus, m *peer.Ledger, takeIssuers bool) {
	if a.Policies == nil {
		return
	}
	m.OnPolicy(func() {
		in := m.View().Policy
		var body []byte
		if in != nil {
			body = []byte(in.Body)
		}
		if err := a.ApplyTroupeDocument(m.Troupe(), body, takeIssuers); err != nil {
			slog.Error("troupe policy in force does not load here; it grants nothing on this node", "version", in.Version, "signer", in.Node, "err", err)
			return
		}
		if in != nil {
			slog.Info("troupe policy in force", "troupe", m.Troupe(), "version", in.Version, "signer", in.Node, "by", in.By)
		}
	})
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
	if a.Peercred == nil {
		return errors.New("troupe.provider figaro: no local principal to own this node")
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
	store.Save(nil, prunable(stored, time.Now()))
	if joined, err := k.Dir.Joined(); err == nil {
		if _, err := m.Merge(joined...); err != nil {
			slog.Warn("troupe ledger: entries from the join refused", "err", err)
		}
		if err := k.Dir.DropJoined(); err != nil {
			slog.Warn("troupe ledger", "err", err)
		}
	}
	a.Peers = m
	bindLedgerTroupePolicy(a, m, loaded.Config.Authz.TakesTroupeIssuers())
	bindLedgerAPIKeys(a, m)
	owner := a.Peercred.Self().String()
	seedJoin(k, m, door.Advertise, owner, time.Now())
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
		view := m.View()
		signers := slices.DeleteFunc(view.Signers(time.Now()), func(n string) bool { return !view.MayIssue(m.Self(), n) || n == m.Self() })
		if len(signers) == 0 {
			return nil, nil, fmt.Errorf("no renewer of this node is known to hold an intermediate (named: %v); the root names one: op read <root ref> | figaro troupe renewer %s %s <node>", view.Renewers[m.Self()], m.Troupe(), m.Self())
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

func seedJoin(k *troupe.Keeper, m *peer.Ledger, door, owner string, now time.Time) {
	inter, key, err := k.Intermediate()
	if err != nil || !now.Before(inter.NotAfter) {
		return
	}
	v, self := m.View(), m.Self()
	if gone := v.Gone[self]; gone != "" {
		slog.Warn("troupe: this node is out of the troupe and does not sign itself back in", "troupe", m.Troupe(), "by", gone)
		return
	}
	mem, ok := v.Members[self]
	if ok && mem.Door == door && mem.SignsUntil.Equal(inter.NotAfter) {
		return
	}
	if ok {
		owner = mem.Owner
	}
	e, err := troupe.SignJoin(key, inter, m.Troupe(), self, door, owner, now)
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
	if id := loaded.TroupeIntermediateKeyCredential(); id != "" {
		if k.IntermediateKey, err = boot.CredentialPath(id); err != nil {
			return nil, fmt.Errorf("troupe.intermediate_key_credential %w", err)
		}
		if path, ok := k.Dir.IntermediateKeyAtRest(); ok {
			slog.Warn("troupe: the intermediate key is read from the credential, yet a copy is at rest in the troupe dir; remove it", "troupe", name, "path", path)
		}
	}
	if id := loaded.TroupeNodeKeyCredential(); id != "" {
		if k.NodeKey, err = boot.CredentialPath(id); err != nil {
			return nil, fmt.Errorf("troupe.node_key_credential %w", err)
		}
		if path, ok := k.Dir.NodeKeyAtRest(); ok {
			slog.Warn("troupe: the node key is read from the credential, yet a copy is at rest in the troupe dir; remove it", "troupe", name, "path", path)
		}
	}
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
	verify := forwardedCallers(authz.Bearers{Issuers: a.Issuers, Keys: a.APIKeys})
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
		Seed:    func() { seedJoin(a.Troupe, m, door, a.Peercred.Self().String(), time.Now()) },
	}
	m.OnEvict(g.Dialer.Evict)
	m.OnAdmit(g.Greet(ctx))
	a.PeerVersions.OnDrift(g.Drift(ctx))
	a.TroupeSpread = g.Spread
	go g.Run(ctx)
	return g
}

func prunable(es []troupe.Entry, now time.Time) []string {
	var out []string
	for _, e := range es {
		if troupe.Prunable(e, now) {
			out = append(out, e.ID())
		}
	}
	return out
}

func forwardedCallers(b authz.Bearers) peer.Verify {
	if b.Issuers == nil && b.Keys == nil {
		slog.Warn("door.tls: no issuers and no api keys, so every forwarded caller is refused")
		return nil
	}
	if b.Issuers == nil || b.Issuers.Empty() {
		slog.Warn("door.tls: no issuer yet, so forwarded callers other than api keys are refused until [[authz.issuers]] or the troupe policy names one")
	}
	return b.Verify
}

func newAPIKeys(loaded *config.Loaded, self string) (*authz.APIKeys, error) {
	name, err := loaded.TroupeName()
	if err != nil {
		return nil, err
	}
	static := loaded.Config.Authz.APIKeys
	if name == "" && len(static) == 0 {
		return nil, nil
	}
	return authz.NewAPIKeys(name, self, static, nil)
}

func bindLedgerAPIKeys(a *angelus.Angelus, m *peer.Ledger) {
	if a.APIKeys == nil {
		return
	}
	m.OnAPIKeys(func(recs map[string]troupe.APIKeyRecord) {
		if err := a.APIKeys.SetTroupe(apiKeyGrants(recs)); err != nil {
			slog.Error("troupe api keys: some are not taken up", "err", err)
		}
	})
}

func apiKeyGrants(recs map[string]troupe.APIKeyRecord) []authz.APIKeyGrant {
	out := make([]authz.APIKeyGrant, 0, len(recs))
	for _, r := range recs {
		g := authz.APIKeyGrant{ID: r.ID, Principal: r.Principal, Hash: r.Hash, NotAfter: r.NotAfter, Nodes: r.Nodes, Void: r.Void}
		if r.Revoked != nil {
			g.Void = fmt.Sprintf("revoked by %s through node %s, %s", r.Revoked.By, r.Revoked.Node, r.Revoked.At.UTC().Format(time.RFC3339))
		}
		out = append(out, g)
	}
	return out
}

func newIssuers(cfgs []config.IssuerConfig) (*authz.Issuers, error) {
	return authz.NewIssuers(cfgs, &http.Client{Timeout: 5 * time.Second}, nil)
}
