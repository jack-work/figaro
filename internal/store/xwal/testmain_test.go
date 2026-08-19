package xwal

import (
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	os.Exit(m.Run())
}

func isTopologyTimeout(err error) bool {
	return err != nil && strings.Contains(err.Error(), "timed out waiting for")
}

func shortTopologyWait(t *testing.T) {
	t.Helper()
	old := topologyWaitTimeout
	topologyWaitTimeout = 50 * time.Millisecond
	t.Cleanup(func() { topologyWaitTimeout = old })
}

func cleanupTrunks(t *testing.T, trunks *Trunks) {
	t.Helper()
	t.Cleanup(func() {
		if err := trunks.Close(); err != nil {
			t.Errorf("close trunks: %v", err)
		}
	})
}

func forkAppend(t *testing.T, f *Trunks, trunk TrunkID, atMainLT uint64, payload []byte) (TrunkID, uint64) {
	t.Helper()
	alt, err := f.ForkAt(trunk, atMainLT)
	if err != nil {
		t.Fatalf("fork %s@%d: %v", trunk, atMainLT, err)
	}
	_, lt, err := f.Append(alt, 0, payload, nil)
	if err != nil {
		t.Fatalf("append after fork: %v", err)
	}
	return alt, lt
}
