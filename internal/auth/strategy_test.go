package auth

import (
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	hush "github.com/jack-work/hush/client"
)

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

// fakeHushAgent serves one canned JSON response per connection.
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
	assert.Contains(t, err.Error(), "figaro provider login anthropic")
}

func TestOAuthInvalidateNilOnSuccessfulRefresh(t *testing.T) {
	client := fakeHushAgent(t, `{"ok":true,"token":"fresh"}`)
	o := &OAuth{Hush: client, Name: "anthropic"}
	assert.NoError(t, o.Invalidate("tok"))
}

func TestOAuthInvalidateNilWhenUnconfigured(t *testing.T) {
	assert.NoError(t, (&OAuth{}).Invalidate("tok"))
}

func TestFileOptionalPassesWhenMissing(t *testing.T) {
	f := &File{Path: filepath.Join(t.TempDir(), "absent")}
	tok, ok, err := f.TryResolve()
	assert.Empty(t, tok)
	assert.False(t, ok)
	assert.NoError(t, err)
}

func TestFileRequiredErrsWhenMissingOrEmpty(t *testing.T) {
	dir := t.TempDir()
	_, ok, err := (&File{Path: filepath.Join(dir, "absent"), Required: true}).TryResolve()
	assert.False(t, ok)
	assert.Error(t, err)

	empty := filepath.Join(dir, "empty")
	require.NoError(t, os.WriteFile(empty, []byte("  \n"), 0o600))
	_, ok, err = (&File{Path: empty, Required: true}).TryResolve()
	assert.False(t, ok)
	assert.ErrorContains(t, err, "empty")
}

func TestFileTrimsTheTrailingNewline(t *testing.T) {
	p := filepath.Join(t.TempDir(), "key")
	require.NoError(t, os.WriteFile(p, []byte("sk-abc\n"), 0o600))
	tok, ok, err := (&File{Path: p}).TryResolve()
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, "sk-abc", tok)
}
