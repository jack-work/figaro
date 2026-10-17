package angelus_test

import (
	"github.com/jack-work/figaro/sdk"
	"os"
	"runtime"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/transport"
)

func testRuntimeDir(t testing.TB, fallback string) string {
	t.Helper()
	if runtime.GOOS != "windows" {
		return fallback
	}
	dir, err := os.MkdirTemp(os.TempDir(), "figaro-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func waitForAngelus(t testing.TB, path string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		client, err := sdk.DialAngelus(transport.UnixEndpoint(path))
		if err == nil {
			_ = client.Close()
			return
		}
		lastErr = err
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("angelus socket %s never accepted a connection: %v", path, lastErr)
}
