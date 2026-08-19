package angelus

// The DORMANT half of study has to mint and retain the libretto exactly as
// the agent's half does, or a figaro studied from the hub is counted by
// nobody: the two halves have had to agree about where the study set lives
// since `set` was served from the hub, and the refcount is the same kind of
// fact.

import (
	"encoding/json"
	"testing"

	"github.com/jack-work/figaro/internal/message"
	"github.com/jack-work/figaro/internal/store"
	"github.com/stretchr/testify/require"
)

func hubStudyFixture(t *testing.T) (*handlers, *store.XwalBackend, string, string) {
	t.Helper()
	be, err := store.NewXwalBackend(t.TempDir(), 0)
	require.NoError(t, err)
	t.Cleanup(func() { be.Close() })

	outfit, err := be.CreateOutfit("hub", message.Patch{
		Set: map[string]json.RawMessage{"system.model": json.RawMessage(`"m"`)},
	})
	require.NoError(t, err)
	aria, err := be.CreateConversation(outfit)
	require.NoError(t, err)
	formID, _, err := be.CreateForm("", message.Patch{
		Set: map[string]json.RawMessage{"brief": json.RawMessage(`"watched"`)},
	})
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

// IMPORT restores a study set by STUDYING, not by copying the key: each id
// goes through the verb, which mints the libretto and retains it. Copying
// the key would declare studies nothing counted -- and cannot, now that
// `system.studies` is system-managed.
func TestImportRestoresStudiesThroughTheVerb(t *testing.T) {
	h, be, _, formID := hubStudyFixture(t)

	// Stand in for the import handler's own sequence: a fresh aria, then the
	// exported board's studies replayed through the verb.
	outfit, err := be.CreateOutfit("imp", message.Patch{
		Set: map[string]json.RawMessage{"system.model": json.RawMessage(`"m"`)},
	})
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
