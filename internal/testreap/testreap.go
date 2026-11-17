package testreap

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const (
	OwnerEnv  = "TESTREAP_OWNER"
	reaperEnv = "TESTREAP_REAPER"
)

var (
	owner  string
	scopes atomic.Int64
	Grace  = 5 * time.Second
)

func Scope(t testing.TB) string {
	t.Helper()
	if owner == "" {
		t.Fatal("testreap.Start was not called in this package's TestMain")
	}
	tag := fmt.Sprintf("%s.%d", owner, scopes.Add(1))
	t.Cleanup(func() {
		if left := outliving(tag, Grace, 0); len(left) > 0 {
			t.Errorf("%d processes outlived %s; ending them:\n%s", len(left), t.Name(), describe(left))
			end(tag, left, 0)
		}
	})
	return OwnerEnv + "=" + tag
}

func Env() string { return OwnerEnv + "=" + owner }

func owns(value, tag string) bool {
	return value == tag || strings.HasPrefix(value, tag+".")
}

func describe(ps []proc) string {
	var b strings.Builder
	for _, p := range ps {
		fmt.Fprintf(&b, "  pid %d: %s\n", p.pid, p.args)
	}
	return b.String()
}

func newOwner() string {
	nonce := make([]byte, 8)
	_, _ = rand.Read(nonce)
	return fmt.Sprintf("%d-%s", os.Getpid(), hex.EncodeToString(nonce))
}
