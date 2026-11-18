package angelus

import (
	"context"
	"encoding/json"
	"slices"
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/rpc"
	"github.com/jack-work/figaro/internal/store"
	"github.com/stretchr/testify/require"
)

// An exported study's keys come back with the study, through the verb.
func TestImportRestoresAStudysKeys(t *testing.T) {
	h, be, _, formID := hubStudyFixture(t)
	studies, _ := json.Marshal([]string{formID})
	req := rpc.ImportRequest{
		Outfit: "imp",
		Form: form.Build(form.Snapshot{}, map[string]json.RawMessage{
			store.StudiesKey:                 studies,
			form.StudyKeysKey + "." + formID: json.RawMessage(`["comments"]`),
		}, nil),
	}
	raw, err := json.Marshal(req)
	require.NoError(t, err)
	out, err := h.importAria(context.Background(), raw)
	require.NoError(t, err)
	id := out.(rpc.ImportResponse).FigaroID

	snap, err := be.FormState(id)
	require.NoError(t, err)
	require.True(t, slices.Equal(form.StudyKeys(snap, formID), []string{"comments"}), "keys after import: %v", form.StudyKeys(snap, formID))
	lib, err := be.Libretto(formID)
	require.NoError(t, err)
	require.Equal(t, 1, lib.Refs())
}

// An export carries the source store's runtime keys; the daemon here writes
// its own, so an import drops them instead of refusing or copying them.
func TestImportDropsTheRuntimeKeysAnExportCarries(t *testing.T) {
	h, be, _, _ := hubStudyFixture(t)
	req := rpc.ImportRequest{
		Outfit: "imp",
		Form: form.Build(form.Snapshot{}, map[string]json.RawMessage{
			form.ForkedFromKey:   json.RawMessage(`"forged"`),
			"system.forked_from": json.RawMessage(`"forged"`),
			form.TombstoneKey:    json.RawMessage(`"deleted"`),
			"mantra":             json.RawMessage(`"kept"`),
		}, nil),
	}
	raw, err := json.Marshal(req)
	require.NoError(t, err)
	out, err := h.importAria(context.Background(), raw)
	require.NoError(t, err)
	snap, err := be.FormState(out.(rpc.ImportResponse).FigaroID)
	require.NoError(t, err)
	require.False(t, form.RuntimeHas(snap, form.ForkedFromKey), "a forged fork parent was imported")
	require.False(t, form.RuntimeHas(snap, form.TombstoneKey), "an imported figaro was born dead")
	v, _ := snap.Get("mantra")
	require.Equal(t, `"kept"`, string(v))
}
