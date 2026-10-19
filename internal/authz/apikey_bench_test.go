package authz

import (
	"context"
	"net/http"
	"testing"
	"time"
)

func benchKeys(b *testing.B) (*APIKeys, string) {
	keys, err := NewAPIKeys("estate", "node-a", nil, nil)
	if err != nil {
		b.Fatal(err)
	}
	var grants []APIKeyGrant
	var wire string
	for i := range 1024 {
		w, k := mintKey(b)
		if i == 512 {
			wire = w
		}
		grants = append(grants, grant(k, "service:ci@troupe:estate", time.Now().Add(time.Hour)))
	}
	if err := keys.SetTroupe(grants); err != nil {
		b.Fatal(err)
	}
	return keys, wire
}

// Opening a key is a connection's cost: parse, one lookup, one hash.
func BenchmarkAPIKeyOpen(b *testing.B) {
	keys, wire := benchKeys(b)
	b.ReportAllocs()
	for b.Loop() {
		if _, err := keys.Open(wire); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkAPIKeyAdmit(b *testing.B) {
	keys, wire := benchKeys(b)
	b.ReportAllocs()
	for b.Loop() {
		ctx, cancel := context.WithCancel(context.Background())
		if _, err := keys.Admit(ctx, wire); err != nil {
			b.Fatal(err)
		}
		cancel()
	}
}

// SubjectOf on a key connection is every RPC's cost.
func BenchmarkSubjectOfAPIKey(b *testing.B) {
	keys, wire := benchKeys(b)
	ctx, err := keys.Admit(context.Background(), wire)
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	for b.Loop() {
		if _, err := SubjectOf(ctx); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkAdmitRequestJWT(b *testing.B) {
	is := newTestIssuer(b)
	keys, _ := benchKeys(b)
	br := Bearers{Issuers: is.bearer(b), Keys: keys}
	r := &http.Request{Header: http.Header{"Authorization": {"Bearer " + is.mint("sandbox-dev-3", "sandbox-dev-3", time.Hour, nil)}}}
	b.ReportAllocs()
	for b.Loop() {
		if _, err := br.AdmitRequest(context.Background(), r); err != nil {
			b.Fatal(err)
		}
	}
}
