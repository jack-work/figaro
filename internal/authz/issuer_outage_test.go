package authz

import (
	"context"
	"testing"
	"time"

	"github.com/jack-work/figaro/internal/config"
)

func TestAConnectedUserKeepsWorkingThroughAnIssuerOutageUntilTheTokenExpires(t *testing.T) {
	is := newTestIssuer(t)
	b, err := NewIssuers([]config.IssuerConfig{is.config()}, nil, is.clock, WithStale(24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	user := map[string]any{"sub": "u-1", "preferred_username": "gluck", "groups": []string{"figaro-admin"}}
	s, err := b.Open(ctx, is.mint("u-1", "figaro-cli", time.Hour, user))
	if err != nil {
		t.Fatal(err)
	}
	is.down.Store(true)
	is.advance(10 * time.Minute)
	for range 3 {
		sub, err := s.Subject(ctx)
		if err != nil || sub.Principal.ID != "gluck@auth.kelliher.info" {
			t.Fatalf("the issuer is down, the token is good for 50 more minutes, and the call was refused: %v %v", sub, err)
		}
	}
	is.advance(time.Hour)
	if _, err := s.Subject(ctx); err == nil {
		t.Fatal("a session outlived its token because the issuer was down")
	}
}

func TestARestartDuringAnIssuerOutageStillAdmitsATokenIssuedBefore(t *testing.T) {
	is := newTestIssuer(t)
	dir := t.TempDir()
	user := map[string]any{"sub": "u-1", "preferred_username": "gluck"}
	token := is.mint("u-1", "figaro-cli", 12*time.Hour, user)
	before, err := NewIssuers([]config.IssuerConfig{is.config()}, nil, is.clock, WithStale(24*time.Hour), WithKeyDir(dir))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := before.Open(context.Background(), token); err != nil {
		t.Fatal(err)
	}
	is.down.Store(true)
	is.advance(2 * time.Hour)
	after, err := NewIssuers([]config.IssuerConfig{is.config()}, nil, is.clock, WithStale(24*time.Hour), WithKeyDir(dir))
	if err != nil {
		t.Fatal(err)
	}
	s, err := after.Open(context.Background(), token)
	if err != nil || s.Principal().ID != "gluck@auth.kelliher.info" {
		t.Fatalf("a daemon restarted while the issuer is down refused a token it could verify before: %v", err)
	}
}

func TestARestartDuringAnOutageDoesNotAdmitAnUnknownUser(t *testing.T) {
	is := newTestIssuer(t)
	dir := t.TempDir()
	first := is.mint("u-1", "figaro-cli", 12*time.Hour, map[string]any{"sub": "u-1", "preferred_username": "gluck"})
	before, err := NewIssuers([]config.IssuerConfig{is.config()}, nil, is.clock, WithStale(24*time.Hour), WithKeyDir(dir))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := before.Open(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	is.down.Store(true)
	stranger := is.mint("u-2", "figaro-cli", 12*time.Hour, map[string]any{"sub": "u-2", "preferred_username": "mallory"})
	after, err := NewIssuers([]config.IssuerConfig{is.config()}, nil, is.clock, WithStale(24*time.Hour), WithKeyDir(dir))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := after.Open(context.Background(), stranger); err == nil {
		t.Fatal("a user nobody resolved before the outage was admitted during it")
	}
}

func TestARestartDuringAnOutageAdmitsNobodyWhenIssuerStaleIsOff(t *testing.T) {
	is := newTestIssuer(t)
	dir := t.TempDir()
	token := is.mint("sandbox-dev-3", "sandbox-dev-3", 12*time.Hour, nil)
	before, err := NewIssuers([]config.IssuerConfig{is.config()}, nil, is.clock, WithKeyDir(dir))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := before.Open(context.Background(), token); err != nil {
		t.Fatal(err)
	}
	is.down.Store(true)
	after, err := NewIssuers([]config.IssuerConfig{is.config()}, nil, is.clock, WithKeyDir(dir))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := after.Open(context.Background(), token); err == nil {
		t.Fatal("issuer_stale = 0 must fail closed, but a restart admitted a token on keys fetched before")
	}
}
