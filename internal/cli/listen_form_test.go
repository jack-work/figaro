package cli

import (
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

// `fig at @form` binds the shell to an unbound form, and `:at @form` in a
// pager shows it, but `figaro listen` (and a bare `figaro`) refused it as
// "a form, not a figaro". listen takes the form itself and opens on its pit;
// a role still opens on the aria that holds it.
func TestListenTakesAnUnboundForm(t *testing.T) {
	backend, aria := store.NewTestAria(t, "listen", message.Patch{})
	acli, dir := serveBackend(t, backend)
	t.Setenv("FIGARO_RUNTIME_DIR", dir)
	t.Setenv("FIGARO_CONFIG_DIR", t.TempDir())
	t.Setenv("FIGARO_TROUPE", "")
	t.Setenv("FIGARO_ARIA", "")
	loaded, err := config.Load(t.TempDir())
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	plain, _, err := backend.CreateForm("", form.Build(form.Snapshot{}, map[string]json.RawMessage{"brief": json.RawMessage(`"x"`)}, nil))
	require.NoError(t, err)
	id, _, err := listenTarget(ctx, loaded, acli, plain, false, dressing{})
	require.NoError(t, err, "listen to an unbound form")
	require.Equal(t, plain, id)
	require.True(t, isFormID(id), "a form opens on its pit")

	held, _ := json.Marshal(aria)
	role, _, err := backend.CreateForm("", form.Build(form.Snapshot{}, map[string]json.RawMessage{"target-aria": held}, nil))
	require.NoError(t, err)
	id, _, err = listenTarget(ctx, loaded, acli, role, false, dressing{})
	require.NoError(t, err, "listen to a role")
	require.Equal(t, aria, id, "a role opens on its holder")
	require.False(t, isFormID(id))
	require.True(t, isFormID("spain2/"+plain), "a form on another node is a form")
}
