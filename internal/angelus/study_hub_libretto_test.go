package angelus

// The dormant half of study must mint and retain the libretto exactly as the agent's half does.

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"

	"github.com/jack-work/figaro/internal/authz"
	"github.com/jack-work/figaro/internal/store"
	"github.com/stretchr/testify/require"
)

func hubStudyFixture(t *testing.T) (*handlers, *store.XwalBackend, string, string) {
	t.Helper()
	be, err := store.NewXwalBackend(t.TempDir(), 0)
	require.NoError(t, err)
	t.Cleanup(func() { be.Close() })

	outfit, err := be.CreateOutfit("hub", form.Build(form.Snapshot{}, map[string]json.RawMessage{"system.model": json.RawMessage(`"m"`)}, nil))
	require.NoError(t, err)
	aria, err := be.CreateConversation(outfit)
	require.NoError(t, err)
	formID, _, err := be.CreateForm("", form.Build(form.Snapshot{}, map[string]json.RawMessage{"brief": json.RawMessage(`"watched"`)}, nil))
	require.NoError(t, err)

	h := &handlers{angelus: &Angelus{Registry: NewRegistry(), Backend: be}}
	return h, be, aria, formID
}

func TestHubStudyRetainsTheLibretto(t *testing.T) {
	h, be, aria, formID := hubStudyFixture(t)

	studies, err := h.studyForHub(aria, formID, false)
	require.NoError(t, err)
	require.Contains(t, studies, formID)

	lib, err := be.Libretto(formID)
	require.NoError(t, err)
	require.Equal(t, 1, lib.Refs(), "the hub declared a study nothing counted")

	// Idempotent: the board is a set, so a repeat is not a second reference.
	_, err = h.studyForHub(aria, formID, false)
	require.NoError(t, err)
	require.Equal(t, 1, lib.Refs(), "a repeated hub study double-counted")

	audit, err := be.ReconcileLibrettos()
	require.NoError(t, err)
	require.Zero(t, audit.Corrected, "the sweep disagreed with the hub: %+v", audit)
}

func TestHubDropReleasesTheLibretto(t *testing.T) {
	h, be, aria, formID := hubStudyFixture(t)
	_, err := h.studyForHub(aria, formID, false)
	require.NoError(t, err)

	studies, err := h.studyForHub(aria, formID, true)
	require.NoError(t, err)
	require.NotContains(t, studies, formID)

	lib, err := be.Libretto(formID)
	require.NoError(t, err)
	require.Equal(t, 0, lib.Refs(), "the hub dropped a study without releasing it")

	// And again: dropping what is not declared moves nothing.
	_, err = h.studyForHub(aria, formID, true)
	require.NoError(t, err)
	require.Equal(t, 0, lib.Refs(), "a repeated hub drop went below zero")

	audit, err := be.ReconcileLibrettos()
	require.NoError(t, err)
	require.Zero(t, audit.Corrected, "the sweep disagreed after a hub drop: %+v", audit)
}

func TestImportRestoresStudiesThroughTheVerb(t *testing.T) {
	h, be, _, formID := hubStudyFixture(t)

	outfit, err := be.CreateOutfit("imp", form.Build(form.Snapshot{}, map[string]json.RawMessage{"system.model": json.RawMessage(`"m"`)}, nil))
	require.NoError(t, err)
	imported, err := be.CreateConversation(outfit)
	require.NoError(t, err)

	_, err = studyThroughStore(be, imported, formID, false)
	require.NoError(t, err)

	lib, err := be.Libretto(formID)
	require.NoError(t, err)
	require.Equal(t, 1, lib.Refs(), "the imported study was not counted")
	studies, err := be.StudiedBy(imported)
	require.NoError(t, err)
	require.Contains(t, studies, formID)

	audit, err := be.ReconcileLibrettos()
	require.NoError(t, err)
	require.Zero(t, audit.Corrected, "the sweep disagreed after an import: %+v", audit)
	_ = h
}

// A role cast onto a dormant aria records who cast it, as the agent's half does.
func TestHubCastMintsTheRoleAsTheCaller(t *testing.T) {
	h, be, aria, _ := hubStudyFixture(t)
	caster := rpc.Principal{Kind: "sandbox", ID: "dev-3"}
	patch := form.Build(form.Snapshot{}, map[string]json.RawMessage{"name": json.RawMessage(`"minted-role"`)}, nil)

	res, err := h.castForHub(authz.WithPrincipal(context.Background(), caster), aria, rpc.CastRequest{RolePatch: &patch})
	require.NoError(t, err)
	require.True(t, res.Patched)

	m, err := be.Meta(res.RoleID)
	require.NoError(t, err)
	require.Equal(t, "sandbox:dev-3", m.MintedBy)
}
