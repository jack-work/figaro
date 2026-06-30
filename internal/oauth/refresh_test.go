package oauth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestRefreshStandard_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("want POST, got %s", r.Method)
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("want application/json content-type, got %q", ct)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		w.Write([]byte(`{"access_token":"newA","refresh_token":"newR","expires_in":3600}`))
	}))
	defer srv.Close()

	cred, err := RefreshStandard(context.Background(), RefreshRequest{
		TokenURL:     srv.URL,
		ClientID:     "cid",
		RefreshToken: "oldR",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cred.Access != "newA" || cred.Refresh != "newR" {
		t.Errorf("unexpected creds: %+v", cred)
	}
	wantExpiresMin := time.Now().Add(3600*time.Second - DefaultSafetyWindow - time.Minute)
	wantExpiresMax := time.Now().Add(3600*time.Second - DefaultSafetyWindow + time.Minute)
	if cred.ExpiresAt.Before(wantExpiresMin) || cred.ExpiresAt.After(wantExpiresMax) {
		t.Errorf("expires_at %v outside window [%v,%v]", cred.ExpiresAt, wantExpiresMin, wantExpiresMax)
	}
}

func TestRefreshStandard_KeepsRefreshTokenWhenOmitted(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"access_token":"newA","expires_in":3600}`))
	}))
	defer srv.Close()

	cred, err := RefreshStandard(context.Background(), RefreshRequest{
		TokenURL:     srv.URL,
		ClientID:     "cid",
		RefreshToken: "stickyR",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cred.Refresh != "stickyR" {
		t.Errorf("want refresh token preserved, got %q", cred.Refresh)
	}
}

func TestRefreshStandard_PermanentOn4xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		w.Write([]byte(`{"error":"invalid_grant"}`))
	}))
	defer srv.Close()

	_, err := RefreshStandard(context.Background(), RefreshRequest{
		TokenURL:     srv.URL,
		ClientID:     "cid",
		RefreshToken: "oldR",
	})
	if !errors.Is(err, ErrRefreshPermanent) {
		t.Fatalf("want ErrRefreshPermanent, got %v", err)
	}
}

func TestRefreshStandard_TransientOn5xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(503)
		w.Write([]byte(`upstream busy`))
	}))
	defer srv.Close()

	_, err := RefreshStandard(context.Background(), RefreshRequest{
		TokenURL:     srv.URL,
		ClientID:     "cid",
		RefreshToken: "oldR",
	})
	if !errors.Is(err, ErrRefreshTransient) {
		t.Fatalf("want ErrRefreshTransient, got %v", err)
	}
}

func TestRefreshStandard_PermanentOnEmptyAccessToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"access_token":"","expires_in":3600}`))
	}))
	defer srv.Close()

	_, err := RefreshStandard(context.Background(), RefreshRequest{
		TokenURL:     srv.URL,
		ClientID:     "cid",
		RefreshToken: "oldR",
	})
	if !errors.Is(err, ErrRefreshPermanent) {
		t.Fatalf("want ErrRefreshPermanent, got %v", err)
	}
}

func TestRefreshStandard_SendsCorrectBody(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, 1024)
		n, _ := r.Body.Read(buf)
		got = string(buf[:n])
		w.Write([]byte(`{"access_token":"a","refresh_token":"r","expires_in":1}`))
	}))
	defer srv.Close()

	_, err := RefreshStandard(context.Background(), RefreshRequest{
		TokenURL:     srv.URL,
		ClientID:     "my-client",
		RefreshToken: "my-refresh",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, want := range []string{`"grant_type":"refresh_token"`, `"client_id":"my-client"`, `"refresh_token":"my-refresh"`} {
		if !strings.Contains(got, want) {
			t.Errorf("body missing %s: %s", want, got)
		}
	}
}

func TestCredential_Expired(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name string
		exp  time.Time
		want bool
	}{
		{"zero never expires", time.Time{}, false},
		{"future not expired", now.Add(time.Minute), false},
		{"past expired", now.Add(-time.Minute), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := (Credential{ExpiresAt: tc.exp}).Expired(now); got != tc.want {
				t.Errorf("want %v, got %v", tc.want, got)
			}
		})
	}
}

func TestCredential_NeedsRefresh(t *testing.T) {
	now := time.Now()
	if (Credential{}).NeedsRefresh(now, time.Minute) {
		t.Error("zero expiry should not need refresh")
	}
	if !(Credential{ExpiresAt: now.Add(30 * time.Second)}).NeedsRefresh(now, time.Minute) {
		t.Error("within window should need refresh")
	}
	if (Credential{ExpiresAt: now.Add(2 * time.Minute)}).NeedsRefresh(now, time.Minute) {
		t.Error("outside window should not need refresh")
	}
}
