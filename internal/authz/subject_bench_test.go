package authz

import (
	"context"
	"testing"
	"time"

	"github.com/jack-work/jkauthz"
)

// SubjectOf on a bearer connection is every RPC's cost on a web door.
func BenchmarkSubjectOfSession(b *testing.B) {
	is := newTestIssuer(b)
	br := is.bearer(b)
	s, err := br.Open(context.Background(), is.mint("u-1", "figaro-cli", time.Hour, map[string]any{"sub": "u-1", "preferred_username": "gluck"}))
	if err != nil {
		b.Fatal(err)
	}
	ctx := WithSession(context.Background(), s)
	b.ReportAllocs()
	for b.Loop() {
		if _, err := SubjectOf(ctx); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkSubjectOfPeercred(b *testing.B) {
	ctx := WithSubject(context.Background(), jkauthz.Subject{Principal: jkauthz.Principal{Kind: jkauthz.KindUser, ID: "1000"}})
	b.ReportAllocs()
	for b.Loop() {
		if _, err := SubjectOf(ctx); err != nil {
			b.Fatal(err)
		}
	}
}
