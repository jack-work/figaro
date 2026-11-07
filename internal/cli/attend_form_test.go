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

// skills/figaro/forms.md writes to a form after `fig at @form`, but attend
// refused an unbound form ("is a form, not a figaro") after binding the
// shell to it anyway. A form is attended as itself; a role still reports
// the aria that holds it.
func TestAttendTakesAnUnboundForm(t *testing.T) {
	backend, aria := store.NewTestAria(t, "attend", message.Patch{})
	acli, dir := serveBackend(t, backend)
	t.Setenv("FIGARO_RUNTIME_DIR", dir)
	t.Setenv("FIGARO_CONFIG_DIR", t.TempDir())
	t.Setenv("FIGARO_TROUPE", "")
	t.Setenv("FIGARO_ARIA", "")
	prev := forceBind
	forceBind = true
	t.Cleanup(func() { forceBind = prev })
	loaded, err := config.Load(t.TempDir())
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	env := verbEnv{loaded: loaded, acli: acli, shellPID: 4242}

	plain, _, err := backend.CreateForm("", form.Build(form.Snapshot{}, map[string]json.RawMessage{"brief": json.RawMessage(`"x"`)}, nil))
	require.NoError(t, err)
	out, err := attendVerb(ctx, env, plain)
	require.NoError(t, err, "attend an unbound form")
	require.Equal(t, plain, out.ID)

	held, _ := json.Marshal(aria)
	role, _, err := backend.CreateForm("", form.Build(form.Snapshot{}, map[string]json.RawMessage{"target-aria": held}, nil))
	require.NoError(t, err)
	out, err = attendVerb(ctx, env, role)
	require.NoError(t, err, "attend a role")
	require.Equal(t, aria, out.ID, "a role reports its holder")
}
