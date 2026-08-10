package angelus_test

import (
	"encoding/json"
	"testing"

	"github.com/jack-work/figaro/internal/rpc"
)

// dress is the birth patch a test means when it says "on outfit mock": the
// layers directive the server materializes.
func dress(t *testing.T, names ...string) *rpc.ChalkboardPatch {
	t.Helper()
	b, err := json.Marshal(names)
	if err != nil {
		t.Fatalf("dress: %v", err)
	}
	return &rpc.ChalkboardPatch{Set: map[string]json.RawMessage{"layers": b}}
}
