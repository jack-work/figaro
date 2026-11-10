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
			store.StudiesKey:                  studies,
			store.StudyKeysKey + "." + formID: json.RawMessage(`["comments"]`),
		}, nil),
	}
	raw, err := json.Marshal(req)
	require.NoError(t, err)
	out, err := h.importAria(context.Background(), raw)
	require.NoError(t, err)
	id := out.(rpc.ImportResponse).FigaroID

	snap, err := be.FormState(id)
	require.NoError(t, err)
	require.True(t, slices.Equal(store.StudyKeys(snap, formID), []string{"comments"}), "keys after import: %v", store.StudyKeys(snap, formID))
	lib, err := be.Libretto(formID)
	require.NoError(t, err)
	require.Equal(t, 1, lib.Refs())
}
