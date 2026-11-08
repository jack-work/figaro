package auth

import (
	"errors"
	"net/http"
	"strings"
	"testing"
)

func TestAnAggregateWithNothingToOfferSaysWhoIsNotSignedIn(t *testing.T) {
	want := &NotSignedInError{Provider: "anthropic"}
	a := &Aggregate{Strategies: []CredentialStrategy{&EnvVar{Name: "FIGARO_TEST_UNSET_CREDENTIAL"}}, Unsigned: func() error { return want }}
	_, err := a.Resolve()
	var got *NotSignedInError
	if !errors.As(err, &got) || got != want {
		t.Fatalf("Resolve err = %v, want the NotSignedInError", err)
	}
}

func TestAMissingSignInReachesTheTurnWithoutAResolvePrefix(t *testing.T) {
	r := &Aggregate{Unsigned: func() error { return &NotSignedInError{Provider: "anthropic"} }}
	_, err := SendRenewing(r, func(string) (*http.Response, error) {
		t.Fatal("sent a request with no credential")
		return nil, nil
	})
	if err == nil || strings.Contains(err.Error(), "resolve token") ||
		err.Error() != "no provider signed in: type :login, or run figaro provider login anthropic" {
		t.Fatalf("err = %v", err)
	}
}
