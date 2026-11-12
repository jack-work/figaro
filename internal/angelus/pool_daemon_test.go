package angelus_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/rpc"
)

const poolConfig = `
[slot.pools.chat-groups]
enabled = true
outfit = "broker-group"
provider = "rou2"
rou2_lease_class = "chat-group"
say_url = "https://broker.example/groups/say"
page_route = "admin"
slots = ["slot0"]
`

// What a pool daemon refuses is decided when it starts: its log level, its
// policy, the doors it never opened. A reload that adds the first pool would
// skip those checks, and one that drops the last would lift them while guest
// figaros may still be in the store.
func TestAReloadCannotMakeOrUnmakeAPoolDaemon(t *testing.T) {
	owner := as("user", strconv.Itoa(os.Getuid()))
	const authzConfig = "[authz]\npolicy = \"policy.toml\"\n"

	plain := newRigWith(t, grantPolicy)
	config := filepath.Join(filepath.Dir(plain.policy), "config.toml")
	if err := os.WriteFile(config, []byte(authzConfig+poolConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := plain.h[rpc.MethodConfigure](owner, json.RawMessage(`{}`)); err == nil || !strings.Contains(err.Error(), "only at start") {
		t.Fatalf("an ordinary daemon reloaded into a pool daemon: %v", err)
	}

	pool := newRigConfigured(t, grantPolicy, poolConfig)
	config = filepath.Join(filepath.Dir(pool.policy), "config.toml")
	if err := os.WriteFile(config, []byte(authzConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.h[rpc.MethodConfigure](owner, json.RawMessage(`{}`)); err == nil || !strings.Contains(err.Error(), "keeps its refusals") {
		t.Fatalf("a pool daemon reloaded into an ordinary one: %v", err)
	}
	off := strings.Replace(poolConfig, "enabled = true", "enabled = false", 1)
	if err := os.WriteFile(config, []byte(authzConfig+off), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.h[rpc.MethodConfigure](owner, json.RawMessage(`{}`)); err != nil {
		t.Fatalf("turning the pool off, which is the kill switch: %v", err)
	}
}
