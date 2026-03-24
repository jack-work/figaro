package jsonrpc_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/internal/jsonrpc"
)

// pipe creates a connected pair of Conns using a unix socket.
func pipe(t *testing.T) (*jsonrpc.Conn, *jsonrpc.Conn) {
	t.Helper()
	dir := t.TempDir()
	sock := filepath.Join(dir, "test.sock")
	ln, err := net.Listen("unix", sock)
	require.NoError(t, err)
	t.Cleanup(func() { ln.Close() })

	serverConn := make(chan net.Conn, 1)
	go func() {
		c, _ := ln.Accept()
		serverConn <- c
	}()

	clientRaw, err := net.Dial("unix", sock)
	require.NoError(t, err)
	serverRaw := <-serverConn

	return jsonrpc.NewConn(clientRaw), jsonrpc.NewConn(serverRaw)
}

// --- Conn tests ---

func TestConn_SendRecv(t *testing.T) {
	client, server := pipe(t)
	defer client.Close()
	defer server.Close()

	msg := jsonrpc.Message{JSONRPC: "2.0", Method: "hello"}
	require.NoError(t, client.Send(msg))

	got, err := server.Recv()
	require.NoError(t, err)
	assert.Equal(t, "hello", got.Method)
}

func TestConn_OrderPreserved(t *testing.T) {
	client, server := pipe(t)
	defer client.Close()
	defer server.Close()

	// Send 100 messages.
	for i := 0; i < 100; i++ {
		id := int64(i)
		require.NoError(t, client.Send(jsonrpc.Message{
			JSONRPC: "2.0", ID: &id, Method: "test",
		}))
	}

	// Receive — must be in order.
	for i := 0; i < 100; i++ {
		msg, err := server.Recv()
		require.NoError(t, err)
		assert.Equal(t, int64(i), *msg.ID)
	}
}

func TestConn_RecvEOF(t *testing.T) {
	client, server := pipe(t)
	defer server.Close()

	client.Close()
	_, err := server.Recv()
	assert.ErrorIs(t, err, io.EOF)
}

// --- Message type tests ---

func TestMessage_Types(t *testing.T) {
	id := int64(1)
	req := jsonrpc.Message{JSONRPC: "2.0", ID: &id, Method: "foo"}
	assert.True(t, req.IsRequest())
	assert.False(t, req.IsResponse())
	assert.False(t, req.IsNotification())

	resp := jsonrpc.Message{JSONRPC: "2.0", ID: &id, Result: json.RawMessage(`"ok"`)}
	assert.False(t, resp.IsRequest())
	assert.True(t, resp.IsResponse())
	assert.False(t, resp.IsNotification())

	notif := jsonrpc.Message{JSONRPC: "2.0", Method: "event"}
	assert.False(t, notif.IsRequest())
	assert.False(t, notif.IsResponse())
	assert.True(t, notif.IsNotification())
}

// --- Server tests ---

func TestServer_HandleRequest(t *testing.T) {
	clientConn, serverConn := pipe(t)
	defer clientConn.Close()

	handlers := map[string]jsonrpc.HandlerFunc{
		"add": func(ctx context.Context, params json.RawMessage) (interface{}, error) {
			var p struct{ A, B int }
			json.Unmarshal(params, &p)
			return p.A + p.B, nil
		},
	}

	srv := jsonrpc.NewServer(serverConn, handlers)
	go srv.Serve(context.Background())
	defer srv.Stop()

	// Send request.
	id := int64(1)
	clientConn.Send(jsonrpc.Message{
		JSONRPC: "2.0", ID: &id, Method: "add",
		Params: json.RawMessage(`{"A":2,"B":3}`),
	})

	// Read response.
	resp, err := clientConn.Recv()
	require.NoError(t, err)
	assert.Equal(t, int64(1), *resp.ID)
	assert.Equal(t, `5`, string(resp.Result))
}

func TestServer_MethodNotFound(t *testing.T) {
	clientConn, serverConn := pipe(t)
	defer clientConn.Close()

	srv := jsonrpc.NewServer(serverConn, map[string]jsonrpc.HandlerFunc{})
	go srv.Serve(context.Background())
	defer srv.Stop()

	id := int64(1)
	clientConn.Send(jsonrpc.Message{
		JSONRPC: "2.0", ID: &id, Method: "nonexistent",
	})

	resp, err := clientConn.Recv()
	require.NoError(t, err)
	assert.NotNil(t, resp.Error)
	assert.Equal(t, -32601, resp.Error.Code)
}

func TestServer_Notify(t *testing.T) {
	clientConn, serverConn := pipe(t)
	defer clientConn.Close()

	srv := jsonrpc.NewServer(serverConn, map[string]jsonrpc.HandlerFunc{})
	go srv.Serve(context.Background())
	defer srv.Stop()

	// Server sends notifications.
	require.NoError(t, srv.Notify("event.one", map[string]string{"data": "first"}))
	require.NoError(t, srv.Notify("event.two", map[string]string{"data": "second"}))

	// Client reads — should be in order.
	msg1, err := clientConn.Recv()
	require.NoError(t, err)
	assert.Equal(t, "event.one", msg1.Method)

	msg2, err := clientConn.Recv()
	require.NoError(t, err)
	assert.Equal(t, "event.two", msg2.Method)
}

func TestServer_NotifyOrderUnder100(t *testing.T) {
	clientConn, serverConn := pipe(t)
	defer clientConn.Close()

	srv := jsonrpc.NewServer(serverConn, map[string]jsonrpc.HandlerFunc{})
	go srv.Serve(context.Background())
	defer srv.Stop()

	// Send 100 notifications rapidly.
	for i := 0; i < 100; i++ {
		require.NoError(t, srv.Notify("delta", map[string]int{"seq": i}))
	}

	// Read all — must be in order.
	for i := 0; i < 100; i++ {
		msg, err := clientConn.Recv()
		require.NoError(t, err)
		var p struct{ Seq int }
		json.Unmarshal(msg.Params, &p)
		assert.Equal(t, i, p.Seq, "notification %d out of order", i)
	}
}

// --- Client tests ---

func TestClient_Call(t *testing.T) {
	clientConn, serverConn := pipe(t)

	handlers := map[string]jsonrpc.HandlerFunc{
		"greet": func(ctx context.Context, params json.RawMessage) (interface{}, error) {
			return "hello", nil
		},
	}

	srv := jsonrpc.NewServer(serverConn, handlers)
	go srv.Serve(context.Background())
	defer srv.Stop()

	client := jsonrpc.NewClient(clientConn, nil)
	defer client.Close()

	var result string
	err := client.Call(context.Background(), "greet", nil, &result)
	require.NoError(t, err)
	assert.Equal(t, "hello", result)
}

func TestClient_CallWithParams(t *testing.T) {
	clientConn, serverConn := pipe(t)

	handlers := map[string]jsonrpc.HandlerFunc{
		"add": func(ctx context.Context, params json.RawMessage) (interface{}, error) {
			var p struct{ A, B int }
			json.Unmarshal(params, &p)
			return p.A + p.B, nil
		},
	}

	srv := jsonrpc.NewServer(serverConn, handlers)
	go srv.Serve(context.Background())
	defer srv.Stop()

	client := jsonrpc.NewClient(clientConn, nil)
	defer client.Close()

	var result int
	err := client.Call(context.Background(), "add", struct{ A, B int }{10, 20}, &result)
	require.NoError(t, err)
	assert.Equal(t, 30, result)
}

func TestClient_CallError(t *testing.T) {
	clientConn, serverConn := pipe(t)

	handlers := map[string]jsonrpc.HandlerFunc{
		"fail": func(ctx context.Context, params json.RawMessage) (interface{}, error) {
			return nil, fmt.Errorf("boom")
		},
	}

	srv := jsonrpc.NewServer(serverConn, handlers)
	go srv.Serve(context.Background())
	defer srv.Stop()

	client := jsonrpc.NewClient(clientConn, nil)
	defer client.Close()

	err := client.Call(context.Background(), "fail", nil, nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "boom")
}

func TestClient_OnNotify(t *testing.T) {
	clientConn, serverConn := pipe(t)

	handlers := map[string]jsonrpc.HandlerFunc{
		"trigger": func(ctx context.Context, params json.RawMessage) (interface{}, error) {
			// Handled by server — the test drives notifications externally.
			return "ok", nil
		},
	}

	srv := jsonrpc.NewServer(serverConn, handlers)
	go srv.Serve(context.Background())
	defer srv.Stop()

	var mu sync.Mutex
	var received []string

	client := jsonrpc.NewClient(clientConn, func(method string, params json.RawMessage) {
		mu.Lock()
		received = append(received, method)
		mu.Unlock()
	})
	defer client.Close()

	// Server sends notifications while the client is connected.
	srv.Notify("event.a", nil)
	srv.Notify("event.b", nil)
	srv.Notify("event.c", nil)

	// Call to synchronize — ensures notifications have been delivered.
	var result string
	client.Call(context.Background(), "trigger", nil, &result)

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, []string{"event.a", "event.b", "event.c"}, received)
}

func TestClient_NotificationsOrdered(t *testing.T) {
	clientConn, serverConn := pipe(t)

	handlers := map[string]jsonrpc.HandlerFunc{
		"done": func(ctx context.Context, params json.RawMessage) (interface{}, error) {
			return true, nil
		},
	}

	srv := jsonrpc.NewServer(serverConn, handlers)
	go srv.Serve(context.Background())
	defer srv.Stop()

	var mu sync.Mutex
	var seqs []int

	client := jsonrpc.NewClient(clientConn, func(method string, params json.RawMessage) {
		var p struct{ Seq int }
		json.Unmarshal(params, &p)
		mu.Lock()
		seqs = append(seqs, p.Seq)
		mu.Unlock()
	})
	defer client.Close()

	// Send 200 notifications.
	for i := 0; i < 200; i++ {
		srv.Notify("delta", map[string]int{"seq": i})
	}

	// Synchronize.
	var result bool
	client.Call(context.Background(), "done", nil, &result)

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, seqs, 200)
	for i, s := range seqs {
		assert.Equal(t, i, s, "notification %d out of order", i)
	}
}

func TestClient_CallTimeout(t *testing.T) {
	clientConn, serverConn := pipe(t)
	defer serverConn.Close()

	// No server — call will block.
	client := jsonrpc.NewClient(clientConn, nil)
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	err := client.Call(ctx, "slow", nil, nil)
	assert.ErrorIs(t, err, context.DeadlineExceeded)
}

func TestClient_ConnectionClosed(t *testing.T) {
	clientConn, serverConn := pipe(t)

	client := jsonrpc.NewClient(clientConn, nil)

	// Close server side — client should detect.
	serverConn.Close()

	// Give read loop time to detect.
	time.Sleep(50 * time.Millisecond)

	err := client.Call(context.Background(), "anything", nil, nil)
	assert.Error(t, err)
	client.Close()
}

// --- Full roundtrip: request + interleaved notifications ---

func TestRoundtrip_RequestWithNotifications(t *testing.T) {
	clientConn, serverConn := pipe(t)

	// The server reference is needed inside the handler to send
	// notifications before returning the response — this is exactly
	// the figaro pattern (agent emits deltas, then returns).
	var srv *jsonrpc.Server

	handlers := map[string]jsonrpc.HandlerFunc{
		"process": func(ctx context.Context, params json.RawMessage) (interface{}, error) {
			// Send notifications before responding.
			srv.Notify("stream.delta", map[string]string{"text": "hello "})
			srv.Notify("stream.delta", map[string]string{"text": "world"})
			srv.Notify("stream.done", nil)
			return "done", nil
		},
	}

	srv = jsonrpc.NewServer(serverConn, handlers)
	go srv.Serve(context.Background())
	defer srv.Stop()

	var mu sync.Mutex
	var notifications []string

	client := jsonrpc.NewClient(clientConn, func(method string, params json.RawMessage) {
		mu.Lock()
		notifications = append(notifications, method)
		mu.Unlock()
	})
	defer client.Close()

	var result string
	err := client.Call(context.Background(), "process", nil, &result)
	require.NoError(t, err)
	assert.Equal(t, "done", result)

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, []string{"stream.delta", "stream.delta", "stream.done"}, notifications)
}
