package transport

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/coder/websocket"
	"github.com/jack-work/jkrpc"
)

type rotatingBearer struct {
	current     atomic.Int32
	invalidated atomic.Int32
}

func (b *rotatingBearer) Resolve() (string, error) {
	return "tok-" + string(rune('0'+b.current.Load())), nil
}

func (b *rotatingBearer) Invalidate(string) error {
	b.invalidated.Add(1)
	b.current.Add(1)
	return nil
}

func echoDoor(t *testing.T, accept string) (*httptest.Server, *atomic.Pointer[string]) {
	t.Helper()
	var seen atomic.Pointer[string]
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := r.Header.Get("Authorization")
		seen.Store(&h)
		if accept != "" && h != "Bearer "+accept {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer c.CloseNow()
		for {
			typ, msg, err := c.Read(context.Background())
			if err != nil {
				return
			}
			if err := c.Write(context.Background(), typ, msg); err != nil {
				return
			}
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &seen
}

func wssURL(srv *httptest.Server) string { return "wss://" + strings.TrimPrefix(srv.URL, "https://") }

// One JSON-RPC frame travels as one text message, and frames survive the round trip intact.
func TestWSSCarriesJSONRPCFramesWithTheBearer(t *testing.T) {
	srv, seen := echoDoor(t, "tok-0")
	ep := WSSEndpoint(wssURL(srv), &rotatingBearer{})
	ep.HTTPClient = srv.Client()

	conn, err := DialWith(ep, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if got := *seen.Load(); got != "Bearer tok-0" {
		t.Fatalf("Authorization = %q", got)
	}
	for i := 1; i <= 3; i++ {
		id := int64(i)
		if err := conn.Send(jkrpc.Message{JSONRPC: "2.0", ID: &id, Method: "figaro.ping"}); err != nil {
			t.Fatal(err)
		}
	}
	for i := 1; i <= 3; i++ {
		msg, err := conn.Recv()
		if err != nil {
			t.Fatal(err)
		}
		if msg.Method != "figaro.ping" || msg.ID == nil || *msg.ID != int64(i) {
			t.Fatalf("frame %d = %+v", i, msg)
		}
	}
}

func TestWSSRetriesOnceAfterA401(t *testing.T) {
	srv, _ := echoDoor(t, "tok-1")
	b := &rotatingBearer{}
	ep := WSSEndpoint(wssURL(srv), b)
	ep.HTTPClient = srv.Client()
	conn, err := DialRaw(ep)
	if err != nil {
		t.Fatalf("dial after one refresh: %v", err)
	}
	conn.Close()
	if b.invalidated.Load() != 1 {
		t.Fatalf("invalidated %d times", b.invalidated.Load())
	}

	srv2, _ := echoDoor(t, "never")
	b2 := &rotatingBearer{}
	ep2 := WSSEndpoint(wssURL(srv2), b2)
	ep2.HTTPClient = srv2.Client()
	if _, err := DialRaw(ep2); err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("err = %v, want a refusal after one retry", err)
	}
	if b2.invalidated.Load() != 1 {
		t.Fatalf("invalidated %d times, want exactly one retry", b2.invalidated.Load())
	}
}

func TestWSSRefusesAPlainURL(t *testing.T) {
	if _, err := DialRaw(WSSEndpoint("ws://example.invalid/", nil)); err == nil {
		t.Fatal("a ws:// url was dialed as wss")
	}
}
