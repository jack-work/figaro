package angelus_test

import (
	"testing"
	"time"

	"github.com/jack-work/figaro/internal/angelus"
	"github.com/jack-work/figaro/internal/transport"
)

func waitForAngelus(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		client, err := angelus.DialClient(transport.UnixEndpoint(path))
		if err == nil {
			_ = client.Close()
			return
		}
		lastErr = err
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("angelus socket %s never accepted a connection: %v", path, lastErr)
}

func waitForFigaro(t *testing.T, endpoint transport.Endpoint) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		conn, err := transport.DialRaw(endpoint)
		if err == nil {
			_ = conn.Close()
			return
		}
		lastErr = err
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("figaro socket %s never accepted a connection: %v", endpoint.Address, lastErr)
}
