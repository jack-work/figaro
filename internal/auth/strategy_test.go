package auth

import (
	"encoding/json"
	"errors"
	"net"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	hush "github.com/jack-work/hush/client"
)

// fakeStrategy is a CredentialStrategy with canned results.
type fakeStrategy struct {
	token       string
	invErr      error
	invalidated int
}

func (f *fakeStrategy) TryResolve() (string, bool, error) {
	return f.token, f.token != "", nil
}

func (f *fakeStrategy) Invalidate(string) error {
	f.invalidated++
	return f.invErr
}

func TestAggregateInvalidatePropagatesStrategyError(t *testing.T) {
	boom := errors.New("refresh rejected")
	bad := &fakeStrategy{invErr: boom}
	ok := &fakeStrategy{}
	agg := &Aggregate{Strategies: []CredentialStrategy{bad, ok}}

	err := agg.Invalidate("tok")
	require.Error(t, err)
	assert.ErrorIs(t, err, boom)
	assert.Equal(t, 1, ok.invalidated, "later strategies still invalidated")
}

func TestAggregateInvalidateNilWhenAllSucceed(t *testing.T) {
	agg := &Aggregate{Strategies: []CredentialStrategy{&fakeStrategy{}, &fakeStrategy{}}}
	assert.NoError(t, agg.Invalidate("tok"))
}

// fakeHushAgent serves one canned JSON response per connection on a
// unix socket, mimicking the hush agent wire protocol.
func fakeHushAgent(t *testing.T, respJSON string) *hush.Client {
	t.Helper()
	sock := filepath.Join(t.TempDir(), "hush.sock")
	ln, err := net.Listen("unix", sock)
	require.NoError(t, err)
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			var req json.RawMessage
			_ = json.NewDecoder(conn).Decode(&req)
			_, _ = conn.Write([]byte(respJSON))
			conn.Close()
		}
	}()
	return hush.NewWithSocket(sock)
}

func TestOAuthInvalidateWrapsPermanentRefreshError(t *testing.T) {
	client := fakeHushAgent(t, `{"ok":false,"error":"refresh token rejected","error_code":"oauth_refresh_permanent"}`)
	o := &OAuth{Hush: client, Name: "anthropic"}

	err := o.Invalidate("tok")
	require.Error(t, err)
	assert.ErrorIs(t, err, hush.ErrOAuthRefreshPermanent)
	assert.Contains(t, err.Error(), "figaro login anthropic")
}

func TestOAuthInvalidateNilOnSuccessfulRefresh(t *testing.T) {
	client := fakeHushAgent(t, `{"ok":true,"token":"fresh"}`)
	o := &OAuth{Hush: client, Name: "anthropic"}
	assert.NoError(t, o.Invalidate("tok"))
}

func TestOAuthInvalidateNilWhenUnconfigured(t *testing.T) {
	assert.NoError(t, (&OAuth{}).Invalidate("tok"))
}
