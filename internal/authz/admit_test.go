package authz

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/jkauthz"
)

func upgrade(h http.Header) *http.Request {
	return &http.Request{Header: h}
}

// The edge's headers become the subject of the upgrade; an upgrade without
// them is no one.
func TestUpstreamAdmitsAnUpgradeByItsHeaders(t *testing.T) {
	u, err := NewUpstream(config.UpstreamConfig{}, "/run/figaro/web.sock", "/run/figaro/angelus.sock", nil)
	if err != nil {
		t.Fatal(err)
	}
	h := http.Header{}
	h.Set("Remote-User", "gluck")
	h.Set("Remote-Groups", "site-fig-access")

	ctx, err := u.AdmitRequest(context.Background(), upgrade(h))
	if err != nil {
		t.Fatal(err)
	}
	s, _ := SubjectOf(ctx)
	if s.Principal != (jkauthz.Principal{Kind: jkauthz.KindUser, ID: "gluck"}) || !s.InGroup("site-fig-access") {
		t.Fatalf("%+v", s)
	}
	if _, err := u.AdmitRequest(context.Background(), upgrade(http.Header{})); !errors.Is(err, ErrUnadmitted) {
		t.Fatalf("no Remote-User: %v", err)
	}
}

// A bearer upgrade binds a session; once its token lapses every call on the
// connection is refused, and an upgrade without a token is no one.
func TestABearerConnectionIsRefusedOnceItsTokenLapses(t *testing.T) {
	is := newIssuer(t)
	b := is.bearer(t)
	h := http.Header{}
	h.Set("Authorization", "Bearer "+is.mint("u-1", "figaro-cli", time.Minute, map[string]any{"sub": "u-1", "preferred_username": "gluck"}))
	ctx, err := b.AdmitRequest(context.Background(), upgrade(h))
	if err != nil {
		t.Fatal(err)
	}
	if s, err := SubjectOf(ctx); err != nil || s.Principal.ID != "gluck" {
		t.Fatalf("%+v %v", s, err)
	}
	if p := PrincipalOf(ctx); p.ID != "gluck" {
		t.Fatalf("hello would report %+v", p)
	}
	is.advance(time.Minute)
	if _, err := SubjectOf(ctx); !errors.Is(err, ErrExpired) {
		t.Fatalf("served past exp: %v", err)
	}
	if _, err := b.AdmitRequest(context.Background(), upgrade(http.Header{})); !errors.Is(err, ErrUnadmitted) {
		t.Fatalf("no token: %v", err)
	}
}
