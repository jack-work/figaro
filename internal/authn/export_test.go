package authn

import (
	"testing"
	"time"
)

type TestIssuer = testIssuer

func TwoIssuers(t *testing.T) (*TestIssuer, *TestIssuer, *Issuers) { return twoIssuers(t) }

func (is *testIssuer) Mint(sub, client string, ttl time.Duration, user map[string]any) string {
	return is.mint(sub, client, ttl, user)
}
