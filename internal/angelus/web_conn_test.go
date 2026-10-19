package angelus

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/require"
)

// What a door admits lives as long as the connection: a key's hold on its
// connection is released when the client goes, not when the key expires.
func TestWebDoorEndsTheAdmittedContextWithTheConnection(t *testing.T) {
	admitted := make(chan context.Context, 1)
	admit := func(ctx context.Context, r *http.Request) (context.Context, error) {
		admitted <- ctx
		return ctx, nil
	}
	path := serveWeb(t, newAriaHub("abcd1234"), Web{Admit: admit})
	c, _, err := dialWeb(t, path, nil)
	require.NoError(t, err)
	ctx := <-admitted
	require.NoError(t, ctx.Err())
	c.Close(websocket.StatusNormalClosure, "")
	select {
	case <-ctx.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("the admitted context outlived its connection")
	}
}
