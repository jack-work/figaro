package authz

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/jkauthz"
	"github.com/jack-work/jkauthz/bearer"
)

var ErrUnadmitted = errors.New("authz: caller not admitted")

func kindOf(name string) (jkauthz.Kind, error) {
	p, err := jkauthz.ParsePrincipal(name + ":x")
	if err != nil || p.Node != "" || string(p.Kind) != name {
		return "", fmt.Errorf("authz: %q is not a principal kind", name)
	}
	return p.Kind, nil
}

type Peercred struct {
	self    jkauthz.Principal
	selfUID uint32
	uids    map[uint32]jkauthz.Principal
}

func NewPeercred(selfUID uint32, cfg config.PeercredConfig) (*Peercred, error) {
	p := &Peercred{
		self:    jkauthz.Principal{Kind: jkauthz.KindUser, ID: strconv.FormatUint(uint64(selfUID), 10)},
		selfUID: selfUID,
		uids:    map[uint32]jkauthz.Principal{},
	}
	for k, v := range cfg.UIDs {
		uid, err := strconv.ParseUint(k, 10, 32)
		if err != nil {
			return nil, fmt.Errorf("authz.peercred.uids: %q is not a uid", k)
		}
		pr, err := jkauthz.ParsePrincipal(v)
		if err != nil {
			return nil, fmt.Errorf("authz.peercred.uids.%s: %w", k, err)
		}
		if pr.Node != "" {
			return nil, fmt.Errorf("authz.peercred.uids.%s: a local uid cannot name another node", k)
		}
		if uint32(uid) == selfUID {
			p.self = pr
			continue
		}
		p.uids[uint32(uid)] = pr
	}
	return p, nil
}

func (p *Peercred) Self() jkauthz.Principal { return p.self }

func (p *Peercred) Subject(uid uint32) (jkauthz.Subject, error) {
	if uid == p.selfUID {
		return jkauthz.Subject{Principal: p.self}, nil
	}
	if pr, ok := p.uids[uid]; ok {
		return jkauthz.Subject{Principal: pr}, nil
	}
	return jkauthz.Subject{}, fmt.Errorf("%w: uid %d", ErrUnadmitted, uid)
}

const (
	headerUser      = "Remote-User"
	headerGroups    = "Remote-Groups"
	headerPrincipal = "Remote-Principal"
	headerClient    = "Remote-Client"
)

type Upstream struct {
	listen   string
	services map[string]jkauthz.Kind
	reverify *Bearer
}

func NewUpstream(cfg config.UpstreamConfig, listen, cliDoor string, reverify *Bearer) (*Upstream, error) {
	if !filepath.IsAbs(listen) {
		return nil, fmt.Errorf("authz.upstream: listener %q: want an absolute unix socket path; headers are trusted only from a socket the edge alone can reach", listen)
	}
	listen = filepath.Clean(listen)
	if cliDoor != "" && listen == filepath.Clean(cliDoor) {
		return nil, fmt.Errorf("authz.upstream: %s is the CLI door; trusted headers there would let any local caller name itself", listen)
	}
	if cfg.Reverify && reverify == nil {
		return nil, fmt.Errorf("authz.upstream.reverify needs [authz.bearer]")
	}
	u := &Upstream{listen: listen, services: map[string]jkauthz.Kind{}}
	if cfg.Reverify {
		u.reverify = reverify
	}
	for client, kind := range cfg.Services {
		k, err := kindOf(kind)
		if err != nil {
			return nil, fmt.Errorf("authz.upstream.services.%s: %w", client, err)
		}
		u.services[client] = k
	}
	return u, nil
}

func (u *Upstream) Listener() string { return u.listen }

func (u *Upstream) Subject(ctx context.Context, h http.Header) (jkauthz.Subject, error) {
	s, err := u.fromHeaders(h)
	if err != nil {
		return jkauthz.Subject{}, err
	}
	if u.reverify == nil {
		return s, nil
	}
	token, ok := bearer.FromHeader(h.Get("Authorization"))
	if !ok {
		if h.Get(headerPrincipal) != "" {
			return jkauthz.Subject{}, fmt.Errorf("%w: the edge verified a bearer it did not forward", ErrUnadmitted)
		}
		return s, nil
	}
	sess, err := u.reverify.Open(ctx, token)
	if err != nil {
		return jkauthz.Subject{}, err
	}
	if sess.Principal() != s.Principal {
		return jkauthz.Subject{}, fmt.Errorf("%w: the edge says %s, the forwarded token is %s", ErrUnadmitted, s.Principal, sess.Principal())
	}
	return s, nil
}

func (u *Upstream) fromHeaders(h http.Header) (jkauthz.Subject, error) {
	user := strings.TrimSpace(h.Get(headerUser))
	if user == "" {
		return jkauthz.Subject{}, fmt.Errorf("%w: no %s", ErrUnadmitted, headerUser)
	}
	switch kind := h.Get(headerPrincipal); kind {
	case "", "human":
		var groups []string
		for _, g := range strings.Split(h.Get(headerGroups), ",") {
			if g = strings.TrimSpace(g); g != "" {
				groups = append(groups, g)
			}
		}
		return jkauthz.Subject{Principal: jkauthz.Principal{Kind: jkauthz.KindUser, ID: user}, Groups: groups}, nil
	case "service":
		client := h.Get(headerClient)
		k, ok := u.services[client]
		if !ok || client != user {
			return jkauthz.Subject{}, fmt.Errorf("%w: service %q", ErrUnadmitted, client)
		}
		return jkauthz.Subject{Principal: jkauthz.Principal{Kind: k, ID: client}}, nil
	default:
		return jkauthz.Subject{}, fmt.Errorf("%w: principal kind %q", ErrUnadmitted, kind)
	}
}

var ErrExpired = errors.New("authz: token expired; present a fresh one")

type Bearer struct {
	verifier *bearer.Verifier
	resolver bearer.Resolver
	now      func() time.Time
}

func NewBearer(cfg config.BearerConfig, client *http.Client, now func() time.Time) (*Bearer, error) {
	if now == nil {
		now = time.Now
	}
	v, err := bearer.NewVerifier(bearer.Config{
		Issuer:   cfg.Issuer,
		JWKSURL:  cfg.JWKS,
		Audience: cfg.Audience,
		Types:    cfg.Types,
		HTTP:     client,
		Now:      now,
	})
	if err != nil {
		return nil, err
	}
	byClient := bearer.ByClient{}
	if len(cfg.Users) > 0 {
		if cfg.Userinfo == "" {
			return nil, fmt.Errorf("authz.bearer: users need a userinfo endpoint")
		}
		ui := &bearer.Userinfo{URL: cfg.Userinfo, HTTP: client, Now: now}
		for _, id := range cfg.Users {
			byClient[id] = ui
		}
	}
	for id, kind := range cfg.Clients {
		k, err := kindOf(kind)
		if err != nil {
			return nil, fmt.Errorf("authz.bearer.clients.%s: %w", id, err)
		}
		if _, dup := byClient[id]; dup {
			return nil, fmt.Errorf("authz.bearer: %s is both a user client and a principal client", id)
		}
		byClient[id] = bearer.Client{Kind: k}
	}
	if len(byClient) == 0 {
		return nil, fmt.Errorf("authz.bearer: no client admitted")
	}
	return &Bearer{verifier: v, resolver: byClient, now: now}, nil
}

type Session struct {
	b         *Bearer
	mu        sync.Mutex
	token     string
	claims    bearer.Claims
	principal jkauthz.Principal
}

func (b *Bearer) Open(ctx context.Context, token string) (*Session, error) {
	c, err := b.verifier.Verify(ctx, token)
	if err != nil {
		return nil, err
	}
	s, err := b.resolver.Resolve(ctx, token, c)
	if err != nil {
		return nil, err
	}
	return &Session{b: b, token: token, claims: c, principal: s.Principal}, nil
}

func (s *Session) Principal() jkauthz.Principal { return s.principal }

func (s *Session) Expiry() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.claims.Expiry
}

func (s *Session) Subject(ctx context.Context) (jkauthz.Subject, error) {
	s.mu.Lock()
	token, claims := s.token, s.claims
	s.mu.Unlock()
	if !s.b.now().Before(claims.Expiry) {
		return jkauthz.Subject{}, ErrExpired
	}
	sub, err := s.b.resolver.Resolve(ctx, token, claims)
	if err != nil {
		return jkauthz.Subject{}, err
	}
	if sub.Principal != s.principal {
		return jkauthz.Subject{}, fmt.Errorf("%w: connection bound to %s, token now resolves to %s", ErrUnadmitted, s.principal, sub.Principal)
	}
	return sub, nil
}

func (s *Session) Present(ctx context.Context, token string) error {
	c, err := s.b.verifier.Verify(ctx, token)
	if err != nil {
		return err
	}
	sub, err := s.b.resolver.Resolve(ctx, token, c)
	if err != nil {
		return err
	}
	if sub.Principal != s.principal {
		return fmt.Errorf("%w: connection bound to %s, new token is %s", ErrUnadmitted, s.principal, sub.Principal)
	}
	s.mu.Lock()
	s.token, s.claims = token, c
	s.mu.Unlock()
	return nil
}
