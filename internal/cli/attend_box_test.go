package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/api/transport"
	"github.com/jack-work/figaro/sdk"
	"github.com/jack-work/jkrpc"
)

// On spain's box0, `figaro new` minted its aria and then exited 1: the shell
// attend is pid.bind, which a box is refused, rightly, since bindings are keyed
// by a pid from another namespace.
func TestNewInsideABoxNamesTheAriaInsteadOfAttending(t *testing.T) {
	dir, err := os.MkdirTemp(os.Getenv("TMPDIR"), "b")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "ep.sock")
	bound := false
	fakeRPCServer(t, sock, map[string]jkrpc.HandlerFunc{
		rpc.MethodHello: func(context.Context, json.RawMessage) (any, error) {
			return rpc.HelloResponse{Version: rpc.ProtocolVersion, Principal: rpc.Principal{Kind: rpc.PrincipalSandbox, ID: "box0@local:box"}}, nil
		},
		rpc.MethodBind: func(context.Context, json.RawMessage) (any, error) {
			bound = true
			return nil, errors.New("pid.bind: sandbox:box0@local:box holds box; none grants admin here")
		},
	})
	acli, err := sdk.DialAngelus(transport.UnixEndpoint(sock))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { acli.Close() })

	t.Setenv("FIGARO_ARIA", "")
	prevForce, prevErr := forceBind, stderrw
	forceBind = true
	var errs bytes.Buffer
	stderrw = &errs
	t.Cleanup(func() { forceBind, stderrw = prevForce, prevErr })

	id := "0123456789abcdef0123456789abcdef"
	if code, exited := captureExit(t, func() { attendNew(context.Background(), acli, id) }); exited {
		t.Fatalf("attendNew exited %d inside a box: %s", code, errs.String())
	}
	if bound {
		t.Fatal("the CLI asked a box endpoint to bind a shell")
	}
	if !strings.Contains(errs.String(), "--id "+id) {
		t.Fatalf("the CLI did not say how to address the aria: %q", errs.String())
	}
}
