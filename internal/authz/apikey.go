package authz

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jack-work/figaro/internal/apikey"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/jkauthz"
)

var (
	errKeyRefused = fmt.Errorf("%w: api key refused", ErrUnadmitted)
	ErrKeyRevoked = errors.New("authz: api key revoked")
)

type APIKeyGrant struct {
	ID        string
	Principal string
	Hash      []byte
	NotAfter  time.Time
	Nodes     []string
	Void      string
}

type APIKeys struct {
	troupe string
	self   string
	now    func() time.Time
	local  map[string]*keyEntry
	set    atomic.Pointer[map[string]*keyEntry]

	mu   sync.Mutex
	live map[string]map[*keyConn]struct{}
}

type keyEntry struct {
	principal jkauthz.Principal
	hash      []byte
	notAfter  time.Time
	nodes     []string
	void      string
}

type keyConn struct {
	cancel context.CancelCauseFunc
}

func NewAPIKeys(troupe, self string, static []config.APIKeyConfig, now func() time.Time) (*APIKeys, error) {
	if now == nil {
		now = time.Now
	}
	if self == "" {
		return nil, errors.New("authz: api keys are checked against this node's id, and it has none")
	}
	k := &APIKeys{troupe: troupe, self: self, now: now, local: map[string]*keyEntry{}, live: map[string]map[*keyConn]struct{}{}}
	if len(static) > 0 && troupe == "" {
		return nil, errors.New("authz.api_keys: keys are scoped by their troupe, and [troupe] name is unset")
	}
	for i, c := range static {
		nodes := c.Nodes
		if len(nodes) == 0 {
			nodes = []string{self}
		}
		e, err := k.entry(APIKeyGrant{ID: c.ID, Principal: c.Principal, NotAfter: c.NotAfter, Nodes: nodes}, c.SHA256)
		if err != nil {
			return nil, fmt.Errorf("authz.api_keys[%d]: %w", i, err)
		}
		if _, dup := k.local[c.ID]; dup {
			return nil, fmt.Errorf("authz.api_keys[%d]: id %s twice", i, c.ID)
		}
		k.local[c.ID] = e
	}
	k.publish(nil)
	return k, nil
}

func (k *APIKeys) entry(g APIKeyGrant, hexHash string) (*keyEntry, error) {
	if err := apikey.ValidID(g.ID); err != nil {
		return nil, err
	}
	p, err := apikey.ParsePrincipal(g.Principal, k.troupe)
	if err != nil {
		return nil, err
	}
	hash := g.Hash
	if hexHash != "" {
		if hash, err = hex.DecodeString(hexHash); err != nil {
			return nil, fmt.Errorf("sha256: %w", err)
		}
	}
	if len(hash) != apikey.HashSize {
		return nil, fmt.Errorf("sha256: want %d bytes", apikey.HashSize)
	}
	if g.NotAfter.IsZero() {
		return nil, errors.New("not_after: every key expires")
	}
	if len(g.Nodes) == 0 {
		return nil, errors.New("nodes: a key names the nodes it is good on")
	}
	return &keyEntry{principal: p, hash: hash, notAfter: g.NotAfter, nodes: g.Nodes, void: g.Void}, nil
}

func (k *APIKeys) publish(troupe map[string]*keyEntry) {
	set := make(map[string]*keyEntry, len(k.local)+len(troupe))
	for id, e := range troupe {
		set[id] = e
	}
	for id, e := range k.local {
		set[id] = e
	}
	k.set.Store(&set)
}

func (k *APIKeys) SetTroupe(grants []APIKeyGrant) error {
	troupe := make(map[string]*keyEntry, len(grants))
	var errs []error
	for _, g := range grants {
		e, err := k.entry(g, "")
		if err != nil {
			errs = append(errs, fmt.Errorf("troupe api key %s: %w", g.ID, err))
			continue
		}
		troupe[g.ID] = e
	}
	k.publish(troupe)
	k.cut()
	return errors.Join(errs...)
}

func (k *APIKeys) cut() {
	set := *k.set.Load()
	k.mu.Lock()
	defer k.mu.Unlock()
	for id, conns := range k.live {
		e := set[id]
		if e != nil && e.void == "" {
			continue
		}
		for c := range conns {
			c.cancel(ErrKeyRevoked)
		}
	}
}

func (k *APIKeys) Open(token string) (*KeySession, error) {
	key, err := apikey.Parse(token)
	if err != nil {
		return nil, errKeyRefused
	}
	e := (*k.set.Load())[key.ID]
	if e == nil || !key.Matches(e.hash) {
		return nil, fmt.Errorf("%w (id %s)", errKeyRefused, key.ID)
	}
	if e.void != "" {
		return nil, fmt.Errorf("%w: %s: %s", ErrKeyRevoked, key.ID, e.void)
	}
	if !k.now().Before(e.notAfter) {
		return nil, fmt.Errorf("%w: api key %s expired %s", ErrExpired, key.ID, e.notAfter.UTC().Format(time.RFC3339))
	}
	if !apikey.Reaches(e.nodes, k.self) {
		return nil, fmt.Errorf("%w: api key %s is not good on node %s", ErrUnadmitted, key.ID, k.self)
	}
	return &KeySession{keys: k, id: key.ID, hash: e.hash, principal: e.principal, nodes: e.nodes, token: token}, nil
}

func (k *APIKeys) Admit(ctx context.Context, token string) (context.Context, error) {
	s, err := k.Open(token)
	if err != nil {
		return nil, err
	}
	cctx, cancel := context.WithCancelCause(ctx)
	c := &keyConn{cancel: cancel}
	k.mu.Lock()
	conns := k.live[s.id]
	if conns == nil {
		conns = map[*keyConn]struct{}{}
		k.live[s.id] = conns
	}
	conns[c] = struct{}{}
	k.mu.Unlock()
	expire := time.AfterFunc(s.expiry().Sub(k.now()), func() { cancel(ErrExpired) })
	context.AfterFunc(cctx, func() {
		expire.Stop()
		k.mu.Lock()
		delete(k.live[s.id], c)
		if len(k.live[s.id]) == 0 {
			delete(k.live, s.id)
		}
		k.mu.Unlock()
	})
	if _, err := s.Subject(); err != nil {
		cancel(err)
		return nil, err
	}
	return context.WithValue(cctx, callerKey{}, caller{subject: jkauthz.Subject{Principal: s.principal}, key: s}), nil
}

func (k *APIKeys) liveConns() int {
	k.mu.Lock()
	defer k.mu.Unlock()
	n := 0
	for _, conns := range k.live {
		n += len(conns)
	}
	return n
}

type KeySession struct {
	keys      *APIKeys
	id        string
	hash      []byte
	principal jkauthz.Principal
	nodes     []string
	token     string
}

func ForwardableTo(ctx context.Context, node string) error {
	c, _ := ctx.Value(callerKey{}).(caller)
	if c.key == nil || apikey.Reaches(c.key.nodes, node) {
		return nil
	}
	return fmt.Errorf("api key %s is not good on node %s (it names %s), so it is not sent there", c.key.id, node, strings.Join(c.key.nodes, ", "))
}

func (s *KeySession) expiry() time.Time {
	if e := (*s.keys.set.Load())[s.id]; e != nil {
		return e.notAfter
	}
	return time.Time{}
}

func (s *KeySession) Subject() (jkauthz.Subject, error) {
	e := (*s.keys.set.Load())[s.id]
	if e == nil || e.void != "" || !bytes.Equal(e.hash, s.hash) {
		return jkauthz.Subject{}, fmt.Errorf("%w: %s", ErrKeyRevoked, s.id)
	}
	if !s.keys.now().Before(e.notAfter) {
		return jkauthz.Subject{}, ErrExpired
	}
	return jkauthz.Subject{Principal: s.principal}, nil
}

type Bearers struct {
	Issuers *Issuers
	Keys    *APIKeys
}

func (b Bearers) Verify(ctx context.Context, token string) (context.Context, error) {
	if apikey.Is(token) {
		if b.Keys == nil {
			return nil, fmt.Errorf("%w: this node takes no api keys", ErrUnadmitted)
		}
		return b.Keys.Admit(ctx, token)
	}
	if b.Issuers == nil {
		return nil, fmt.Errorf("%w: this node trusts no issuer", ErrUnadmitted)
	}
	s, err := b.Issuers.Open(ctx, token)
	if err != nil {
		return nil, err
	}
	return WithSession(ctx, s), nil
}
