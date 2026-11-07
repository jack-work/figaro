package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/internal/config"
	"github.com/jack-work/figaro/internal/store"
)

// A role holding its aria by word handle printed target-aria as
// "<handle> (missing)": status matched the holder against full ids only.
// The handle names the aria as it does for send.
func TestStatusOfARoleFindsAHolderNamedByHandle(t *testing.T) {
	backend, id := store.NewTestAria(t, "status", message.Patch{})
	acli, dir := serveBackend(t, backend)
	t.Setenv("FIGARO_RUNTIME_DIR", dir)
	t.Setenv("FIGARO_CONFIG_DIR", t.TempDir())
	t.Setenv("FIGARO_TROUPE", "")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	list, err := acli.ListGlobal(ctx)
	require.NoError(t, err)
	handle := ""
	for _, r := range list.Figaros {
		if r.ID == id {
			handle = r.Handle
		}
	}
	require.NotEmpty(t, handle, "the test aria has no handle")

	for _, ref := range []string{handle, id[:8]} {
		raw, _ := json.Marshal(ref)
		formID, _, err := backend.CreateForm("", form.Build(form.Snapshot{}, map[string]json.RawMessage{"target-aria": raw}, nil))
		require.NoError(t, err)
		loaded, err := config.Load(t.TempDir())
		require.NoError(t, err)
		var out bytes.Buffer
		prev := stdout
		stdout = &out
		runStatus(loaded, "", []string{formID}, false, true)
		stdout = prev
		var st map[string]any
		require.NoError(t, json.Unmarshal(out.Bytes(), &st), out.String())
		require.NotEqual(t, "missing", st["target_state"], "role holding %q: %s", ref, out.String())
	}
}
