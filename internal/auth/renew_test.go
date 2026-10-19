package auth

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

type rotating struct {
	tokens      []string
	invalidated []string
	invErr      error
	resolveErr  error
}

func (r *rotating) Resolve() (string, error) {
	if len(r.invalidated) > 0 && r.resolveErr != nil {
		return "", r.resolveErr
	}
	return r.tokens[min(len(r.invalidated), len(r.tokens)-1)], nil
}

func (r *rotating) Invalidate(t string) error {
	r.invalidated = append(r.invalidated, t)
	return r.invErr
}

func gate(accept string, sent *[]string) func(string) (*http.Response, error) {
	return func(token string) (*http.Response, error) {
		*sent = append(*sent, token)
		status := http.StatusOK
		if token != accept {
			status = http.StatusUnauthorized
		}
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader("rejected " + token))}, nil
	}
}

// A token the server has revoked must not stand until it expires.
func TestSendRenewingReplacesARejectedTokenOnce(t *testing.T) {
	r := &rotating{tokens: []string{"stale", "fresh"}}
	var sent []string
	resp, err := SendRenewing(r, gate("fresh", &sent))
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("resp = %v, err = %v", resp, err)
	}
	if strings.Join(sent, ",") != "stale,fresh" || strings.Join(r.invalidated, ",") != "stale" {
		t.Fatalf("sent %v, invalidated %v", sent, r.invalidated)
	}
}

// One retry, not a loop: a second 401 is the server's answer.
func TestSendRenewingRetriesOnlyOnce(t *testing.T) {
	r := &rotating{tokens: []string{"a", "b", "c"}}
	var sent []string
	resp, err := SendRenewing(r, gate("c", &sent))
	if err != nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("resp = %v, err = %v", resp, err)
	}
	if len(sent) != 2 {
		t.Fatalf("sent %v", sent)
	}
}

// The caller reports the 401 with its body when nothing better is available.
func TestSendRenewingHandsBackThe401WhenTheTokenCannotChange(t *testing.T) {
	for name, r := range map[string]*rotating{
		"same token":        {tokens: []string{"k"}},
		"invalidate failed": {tokens: []string{"k", "other"}, invErr: errors.New("static")},
	} {
		var sent []string
		resp, err := SendRenewing(r, gate("never", &sent))
		if err != nil || resp.StatusCode != http.StatusUnauthorized || len(sent) != 1 {
			t.Fatalf("%s: resp = %v, err = %v, sent %v", name, resp, err, sent)
		}
		body, _ := io.ReadAll(resp.Body)
		if string(body) != "rejected k" {
			t.Fatalf("%s: body %q", name, body)
		}
	}
}

func TestSendRenewingNamesWhyNoFreshTokenCame(t *testing.T) {
	r := &rotating{tokens: []string{"stale"}, resolveErr: errors.New("token endpoint down")}
	var sent []string
	_, err := SendRenewing(r, gate("fresh", &sent))
	if err == nil || !strings.Contains(err.Error(), "token endpoint down") || !strings.Contains(err.Error(), "401") {
		t.Fatalf("err = %v", err)
	}
}

// An anonymous request has no token to replace.
func TestSendRenewingLeavesAnAnonymous401Alone(t *testing.T) {
	r := &rotating{tokens: []string{""}}
	var sent []string
	resp, err := SendRenewing(r, gate("x", &sent))
	if err != nil || resp.StatusCode != http.StatusUnauthorized || len(r.invalidated) != 0 {
		t.Fatalf("resp = %v, err = %v, invalidated %v", resp, err, r.invalidated)
	}
}
