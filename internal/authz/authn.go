package authz

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
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

var errTroupeKind = errors.New("authz: troupe is the kind a node's policy delegates to; no caller may be one")

func kindOf(name string) (jkauthz.Kind, error) {
	p, err := jkauthz.ParsePrincipal(name + ":x")
	if err != nil || p.Node != "" || string(p.Kind) != name {
		return "", fmt.Errorf("authz: %q is not a principal kind", name)
	}
	if p.Kind == KindTroupe {
		return "", errTroupeKind
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
		if pr.Kind == KindTroupe {
			return nil, fmt.Errorf("authz.peercred.uids.%s: %w", k, errTroupeKind)
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
	scope    string
	services map[string]jkauthz.Kind
	reverify *Issuers
}

func NewUpstream(cfg config.UpstreamConfig, listen, cliDoor string, reverify *Issuers) (*Upstream, error) {
	if !filepath.IsAbs(listen) {
		return nil, fmt.Errorf("authz.upstream: listener %q: want an absolute unix socket path; headers are trusted only from a socket the edge alone can reach", listen)
	}
	listen = filepath.Clean(listen)
	if cliDoor != "" && listen == filepath.Clean(cliDoor) {
		return nil, fmt.Errorf("authz.upstream: %s is the CLI door; trusted headers there would let any local caller name itself", listen)
	}
	if cfg.Issuer == "" {
		return nil, fmt.Errorf("authz.upstream.issuer: name the issuer the edge vouches for")
	}
	scope, err := IssuerScope(cfg.Issuer)
	if err != nil {
		return nil, fmt.Errorf("authz.upstream.issuer: %w", err)
	}
	if cfg.Reverify && (reverify == nil || !reverify.Trusts(cfg.Issuer)) {
		return nil, fmt.Errorf("authz.upstream.reverify needs %s among [[authz.issuers]]", cfg.Issuer)
	}
	u := &Upstream{listen: listen, scope: scope, services: map[string]jkauthz.Kind{}}
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
	s, _, err := u.subject(ctx, h)
	return s, err
}

func (u *Upstream) subject(ctx context.Context, h http.Header) (jkauthz.Subject, string, error) {
	s, err := u.fromHeaders(h)
	if err != nil {
		return jkauthz.Subject{}, "", err
	}
	if u.reverify == nil {
		return s, "", nil
	}
	token, ok := bearer.FromHeader(h.Get("Authorization"))
	if !ok {
		if h.Get(headerPrincipal) != "" {
			return jkauthz.Subject{}, "", fmt.Errorf("%w: the edge verified a bearer it did not forward", ErrUnadmitted)
		}
		return s, "", nil
	}
	sess, err := u.reverify.Open(ctx, token)
	if err != nil {
		return jkauthz.Subject{}, "", err
	}
	if sess.Principal() != s.Principal {
		return jkauthz.Subject{}, "", fmt.Errorf("%w: the edge says %s, the forwarded token is %s", ErrUnadmitted, s.Principal, sess.Principal())
	}
	return s, token, nil
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
		return scoped(jkauthz.Subject{Principal: jkauthz.Principal{Kind: jkauthz.KindUser, ID: user}, Groups: groups}, u.scope), nil
	case "service":
		client := h.Get(headerClient)
		k, ok := u.services[client]
		if !ok || client != user {
			return jkauthz.Subject{}, fmt.Errorf("%w: service %q", ErrUnadmitted, client)
		}
		return scoped(jkauthz.Subject{Principal: jkauthz.Principal{Kind: k, ID: client}}, u.scope), nil
	default:
		return jkauthz.Subject{}, fmt.Errorf("%w: principal kind %q", ErrUnadmitted, kind)
	}
}

var ErrExpired = errors.New("authz: token expired; present a fresh one")

type Issuers struct {
	byURL map[string]*issuer
	only  *issuer
}

type issuer struct {
	scope    string
	verifier *bearer.Verifier
	resolver bearer.Resolver
	now      func() time.Time
}

func NewIssuers(cfgs []config.IssuerConfig, client *http.Client, now func() time.Time) (*Issuers, error) {
	if len(cfgs) == 0 {
		return nil, fmt.Errorf("authz.issuers: none configured")
	}
	if now == nil {
		now = time.Now
	}
	is := &Issuers{byURL: map[string]*issuer{}}
	scopes := map[string]string{}
	for _, cfg := range cfgs {
		iss, err := newIssuer(cfg, client, now)
		if err != nil {
			return nil, err
		}
		if prev, dup := scopes[iss.scope]; dup {
			return nil, fmt.Errorf("authz.issuers: %s and %s both scope principals as @%s", prev, cfg.Issuer, iss.scope)
		}
		scopes[iss.scope] = cfg.Issuer
		is.byURL[cfg.Issuer] = iss
		is.only = iss
	}
	if len(is.byURL) > 1 {
		is.only = nil
	}
	return is, nil
}

func newIssuer(cfg config.IssuerConfig, client *http.Client, now func() time.Time) (*issuer, error) {
	scope, err := IssuerScope(cfg.Issuer)
	if err != nil {
		return nil, fmt.Errorf("authz.issuers: %w", err)
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
		return nil, fmt.Errorf("authz.issuers %s: %w", cfg.Issuer, err)
	}
	byClient := bearer.ByClient{}
	if len(cfg.Users) > 0 {
		if cfg.Userinfo == "" {
			return nil, fmt.Errorf("authz.issuers %s: users need a userinfo endpoint", cfg.Issuer)
		}
		ui := &bearer.Userinfo{URL: cfg.Userinfo, HTTP: client, Now: now}
		for _, id := range cfg.Users {
			byClient[id] = ui
		}
	}
	for id, kind := range cfg.Clients {
		k, err := kindOf(kind)
		if err != nil {
			return nil, fmt.Errorf("authz.issuers %s: clients.%s: %w", cfg.Issuer, id, err)
		}
		if _, dup := byClient[id]; dup {
			return nil, fmt.Errorf("authz.issuers %s: %s is both a user client and a principal client", cfg.Issuer, id)
		}
		byClient[id] = bearer.Client{Kind: k}
	}
	if len(byClient) == 0 {
		return nil, fmt.Errorf("authz.issuers %s: no client admitted", cfg.Issuer)
	}
	return &issuer{scope: scope, verifier: v, resolver: byClient, now: now}, nil
}

func IssuerScope(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("issuer %q: %w", raw, err)
	}
	if (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("issuer %q: want an http(s) URL with a host, no user, query or fragment", raw)
	}
	scope := u.Host + strings.TrimSuffix(u.EscapedPath(), "/")
	if strings.ContainsAny(scope, "@* \t") {
		return "", fmt.Errorf("issuer %q: scope %q contains a reserved character", raw, scope)
	}
	return scope, nil
}

func (is *Issuers) Trusts(url string) bool {
	_, ok := is.byURL[url]
	return ok
}

func (is *Issuers) issuerFor(token string) (*issuer, error) {
	if is.only != nil {
		return is.only, nil
	}
	iss := unverifiedIssuer(token)
	if i, ok := is.byURL[iss]; ok {
		return i, nil
	}
	return nil, fmt.Errorf("%w: token issuer %.128q is not trusted here", ErrUnadmitted, iss)
}

func unverifiedIssuer(token string) string {
	_, rest, _ := strings.Cut(token, ".")
	payload, _, _ := strings.Cut(rest, ".")
	raw, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		return ""
	}
	var c struct {
		Iss string `json:"iss"`
	}
	if json.Unmarshal(raw, &c) != nil {
		return ""
	}
	return c.Iss
}

func (i *issuer) resolve(ctx context.Context, token string, c bearer.Claims) (jkauthz.Subject, error) {
	s, err := i.resolver.Resolve(ctx, token, c)
	if err != nil {
		return jkauthz.Subject{}, err
	}
	return scoped(s, i.scope), nil
}

func scoped(s jkauthz.Subject, scope string) jkauthz.Subject {
	s.Principal.ID += "@" + scope
	s.Groups = scopedGroups(s.Groups, scope)
	return s
}

func scopedGroups(groups []string, scope string) []string {
	if len(groups) == 0 {
		return nil
	}
	out := make([]string, len(groups))
	for n, g := range groups {
		out[n] = g + "@" + scope
	}
	return out
}

type Session struct {
	iss       *issuer
	bare      jkauthz.Principal
	principal jkauthz.Principal
	mu        sync.Mutex
	token     string
	claims    bearer.Claims
	rawGroups []string
	groups    []string
}

func (is *Issuers) Open(ctx context.Context, token string) (*Session, error) {
	i, err := is.issuerFor(token)
	if err != nil {
		return nil, err
	}
	c, err := i.verifier.Verify(ctx, token)
	if err != nil {
		return nil, err
	}
	raw, err := i.resolver.Resolve(ctx, token, c)
	if err != nil {
		return nil, err
	}
	return &Session{iss: i, bare: raw.Principal, principal: scoped(jkauthz.Subject{Principal: raw.Principal}, i.scope).Principal, token: token, claims: c}, nil
}

func (s *Session) Principal() jkauthz.Principal { return s.principal }

func (s *Session) Token() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.token
}

func (s *Session) Expiry() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.claims.Expiry
}

func (s *Session) Subject(ctx context.Context) (jkauthz.Subject, error) {
	s.mu.Lock()
	token, claims := s.token, s.claims
	s.mu.Unlock()
	if !s.iss.now().Before(claims.Expiry) {
		return jkauthz.Subject{}, ErrExpired
	}
	raw, err := s.iss.resolver.Resolve(ctx, token, claims)
	if err != nil {
		return jkauthz.Subject{}, err
	}
	if raw.Principal != s.bare {
		return jkauthz.Subject{}, fmt.Errorf("%w: connection bound to %s, token now resolves to %s", ErrUnadmitted, s.principal, scoped(raw, s.iss.scope).Principal)
	}
	return jkauthz.Subject{Principal: s.principal, Groups: s.groupsOf(raw.Groups)}, nil
}

func (s *Session) groupsOf(raw []string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(raw) != len(s.rawGroups) || (len(raw) > 0 && &raw[0] != &s.rawGroups[0]) {
		s.rawGroups, s.groups = raw, scopedGroups(raw, s.iss.scope)
	}
	return s.groups
}

func (s *Session) Present(ctx context.Context, token string) error {
	c, err := s.iss.verifier.Verify(ctx, token)
	if err != nil {
		return err
	}
	sub, err := s.iss.resolve(ctx, token, c)
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

func (p *Peercred) Admit(ctx context.Context, conn net.Conn) (context.Context, error) {
	uid, err := PeerUID(conn)
	if err != nil {
		return nil, err
	}
	s, err := p.Subject(uid)
	if err != nil {
		return nil, err
	}
	return WithSubject(ctx, s), nil
}

func (u *Upstream) AdmitRequest(ctx context.Context, r *http.Request) (context.Context, error) {
	s, token, err := u.subject(ctx, r.Header)
	if err != nil {
		return nil, err
	}
	if token != "" {
		return WithVerifiedBearer(ctx, s, token), nil
	}
	return WithSubject(ctx, s), nil
}

func (is *Issuers) AdmitRequest(ctx context.Context, r *http.Request) (context.Context, error) {
	token, ok := bearer.FromHeader(r.Header.Get("Authorization"))
	if !ok {
		return nil, fmt.Errorf("%w: no bearer token", ErrUnadmitted)
	}
	s, err := is.Open(ctx, token)
	if err != nil {
		return nil, err
	}
	return WithSession(ctx, s), nil
}
