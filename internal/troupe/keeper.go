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

type Keeper struct {
	Dir    Dir
	Troupe string
	Node   string
	Now    func() time.Time
	TTL    time.Duration

	mu     sync.Mutex
	cur    *tls.Certificate
	roots  *x509.CertPool
	status Status
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
	return fmt.Sprintf("op read <root ref> | figaro troupe intermediate %s", k.Troupe)
}

func (k *Keeper) Ensure() error {
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
		return fmt.Errorf("troupe %s: this node holds no root certificate; run: %s", k.Troupe, k.remedy())
	}
	if err != nil {
		return fmt.Errorf("troupe %s: %w", k.Troupe, err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(root)
	k.mu.Lock()
	k.roots = pool
	k.mu.Unlock()
	inter, ikey, err := k.Dir.Intermediate()
	if errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("troupe %s: this node has no intermediate; run: %s", k.Troupe, k.remedy())
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
	life := c.Leaf.NotAfter.Sub(c.Leaf.NotBefore)
	return c.Leaf.NotAfter.Sub(now) > life/3
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
