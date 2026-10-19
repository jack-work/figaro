package troupe

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"sync"
	"time"
)

const checkEvery = time.Hour

type Status struct {
	Troupe              string
	NodeExpires         time.Time
	IntermediateExpires time.Time
	Err                 error
}

type Renew func(pub ed25519.PublicKey) (leaf, issuer *x509.Certificate, err error)

type Keeper struct {
	Dir    Dir
	Troupe string
	Node   string
	Now    func() time.Time
	TTL    time.Duration
	Renew  Renew

	IntermediateKey string

	ensuring sync.Mutex
	mu       sync.Mutex
	cur      *tls.Certificate
	roots    *x509.CertPool
	status   Status
}

func (k *Keeper) now() time.Time {
	if k.Now == nil {
		return time.Now()
	}
	return k.Now()
}

func (k *Keeper) ttl() time.Duration {
	if k.TTL <= 0 {
		return NodeTTL
	}
	return k.TTL
}

func (k *Keeper) remedy() string {
	if k.IntermediateKey != "" {
		return fmt.Sprintf("op read <root ref> | figaro troupe intermediate %s --for %s --out <tmpfs dir> on the founder, then deliver intermediate.key to %s and intermediate.crt into %s",
			k.Troupe, k.Node, k.IntermediateKey, k.Dir)
	}
	return fmt.Sprintf("op read <root ref> | figaro troupe intermediate %s", k.Troupe)
}

func (k *Keeper) Intermediate() (*x509.Certificate, ed25519.PrivateKey, error) {
	if k.IntermediateKey == "" {
		return k.Dir.Intermediate()
	}
	cert, key, err := k.Dir.IntermediateWithKey(k.IntermediateKey)
	if err != nil {
		return nil, nil, fmt.Errorf("the intermediate from the credential: %v; to replace it: %s", err, k.remedy())
	}
	return cert, key, nil
}

func (k *Keeper) rejoin() string {
	return fmt.Sprintf("figaro troupe join %s", k.Troupe)
}

func (k *Keeper) Ensure() error {
	k.ensuring.Lock()
	defer k.ensuring.Unlock()
	now := k.now()
	err := k.ensure(now)
	k.mu.Lock()
	defer k.mu.Unlock()
	k.status.Troupe, k.status.Err = k.Troupe, err
	if err != nil && k.cur != nil && !now.Before(k.cur.Leaf.NotAfter) {
		k.cur = nil
	}
	if k.cur != nil {
		k.status.NodeExpires = k.cur.Leaf.NotAfter
	}
	return err
}

func (k *Keeper) ensure(now time.Time) error {
	root, err := k.Dir.Root()
	if errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("troupe %s: this node holds no root certificate; run: %s, or %s", k.Troupe, k.rejoin(), k.remedy())
	}
	if err != nil {
		return fmt.Errorf("troupe %s: %w", k.Troupe, err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(root)
	k.mu.Lock()
	k.roots = pool
	k.mu.Unlock()
	inter, ikey, err := k.Intermediate()
	if errors.Is(err, fs.ErrNotExist) {
		return k.ensureLeaf(root, now)
	}
	if err != nil {
		return fmt.Errorf("troupe %s: %w", k.Troupe, err)
	}
	k.mu.Lock()
	k.status.IntermediateExpires = inter.NotAfter
	k.mu.Unlock()
	if !now.Before(inter.NotAfter) {
		return fmt.Errorf("troupe %s: %w at %s; the node door refuses peers until you run: %s",
			k.Troupe, ErrIntermediateExpired, inter.NotAfter.Format(time.RFC3339), k.remedy())
	}
	held, err := k.Dir.Node()
	if err == nil && k.fresh(held, inter.Raw, now) {
		k.keep(&held)
		return nil
	}
	_, nk, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	leaf, err := IssueNode(ikey, inter, k.Troupe, k.Node, nk.Public(), now, k.ttl())
	if err != nil {
		return fmt.Errorf("troupe %s: %w", k.Troupe, err)
	}
	if err := k.Dir.WriteNode(leaf, inter, nk); err != nil {
		return err
	}
	pair, err := k.Dir.Node()
	if err != nil {
		return err
	}
	slog.Info("troupe node certificate issued", "troupe", k.Troupe, "node", k.Node, "until", leaf.NotAfter.Format(time.RFC3339))
	k.keep(&pair)
	return nil
}

func (k *Keeper) fresh(c tls.Certificate, interRaw []byte, now time.Time) bool {
	if len(c.Certificate) < 2 || string(c.Certificate[1]) != string(interRaw) {
		return false
	}
	return unspent(c.Leaf, now)
}

func unspent(leaf *x509.Certificate, now time.Time) bool {
	life := leaf.NotAfter.Sub(leaf.NotBefore)
	return leaf.NotAfter.Sub(now) > life/3
}

func (k *Keeper) ensureLeaf(root *x509.Certificate, now time.Time) error {
	held, err := k.Dir.Node()
	if err != nil {
		return fmt.Errorf("troupe %s: this node holds neither an intermediate nor a node certificate; run: %s", k.Troupe, k.rejoin())
	}
	if unspent(held.Leaf, now) {
		k.keep(&held)
		return nil
	}
	err = errors.New("no signer to renew from")
	if k.Renew != nil {
		err = k.renewFromPeer(root, now)
		if err == nil {
			return nil
		}
	}
	if now.Before(held.Leaf.NotAfter) {
		k.keep(&held)
		return fmt.Errorf("troupe %s: the node certificate (until %s) is due and was not renewed: %w", k.Troupe, held.Leaf.NotAfter.Format(time.RFC3339), err)
	}
	return fmt.Errorf("troupe %s: the node certificate expired at %s and was not renewed (%v); run: %s",
		k.Troupe, held.Leaf.NotAfter.Format(time.RFC3339), err, k.rejoin())
}

func (k *Keeper) renewFromPeer(root *x509.Certificate, now time.Time) error {
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	leaf, issuer, err := k.Renew(pub)
	if err != nil {
		return err
	}
	if err := CheckLeaf(leaf, issuer, root, k.Troupe, k.Node, pub, now); err != nil {
		return fmt.Errorf("the renewed certificate: %w", err)
	}
	if err := k.Dir.WriteNode(leaf, issuer, key); err != nil {
		return err
	}
	pair, err := k.Dir.Node()
	if err != nil {
		return err
	}
	slog.Info("troupe node certificate renewed by a peer", "troupe", k.Troupe, "node", k.Node, "issuer", IntermediateNode(issuer), "until", leaf.NotAfter.Format(time.RFC3339))
	k.keep(&pair)
	return nil
}

func CheckLeaf(leaf, issuer, root *x509.Certificate, troupe, node string, pub ed25519.PublicKey, now time.Time) error {
	if got, ok := leaf.PublicKey.(ed25519.PublicKey); !ok || !got.Equal(pub) {
		return errors.New("it does not carry the key this node sent")
	}
	if !names(leaf, Identity(troupe, node)) {
		return fmt.Errorf("it does not name %s", Identity(troupe, node))
	}
	roots := x509.NewCertPool()
	roots.AddCert(root)
	inter := x509.NewCertPool()
	inter.AddCert(issuer)
	_, err := leaf.Verify(x509.VerifyOptions{Roots: roots, Intermediates: inter, CurrentTime: now, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}})
	return err
}

func (k *Keeper) keep(c *tls.Certificate) {
	k.mu.Lock()
	k.cur = c
	k.mu.Unlock()
}

func (k *Keeper) Current() (*tls.Certificate, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.cur == nil || !k.now().Before(k.cur.Leaf.NotAfter) {
		if k.status.Err != nil {
			return nil, k.status.Err
		}
		return nil, fmt.Errorf("troupe %s: no current node certificate", k.Troupe)
	}
	return k.cur, nil
}

func (k *Keeper) Roots() (*x509.CertPool, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.roots == nil {
		return nil, fmt.Errorf("troupe %s: no root certificate loaded", k.Troupe)
	}
	return k.roots, nil
}

func (k *Keeper) Status() Status {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.status
}

func (k *Keeper) Run(ctx context.Context) {
	t := time.NewTicker(checkEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := k.Ensure(); err != nil {
				slog.Error("troupe certificate", "err", err)
			}
		}
	}
}
