package troupe

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

// A join's endpoint is signed, stored and gossiped under the key "door", the
// name it had when the estate's entries were signed. Renaming the key changes
// the signed bytes and every entry ID, so it waits for an entry format v2
// that re-signs every join.
func TestAJoinIsSignedUnderTheKeyItWasSignedUnderBefore069(t *testing.T) {
	e := Entry{Kind: Join, Troupe: "estate", Node: "n1", Endpoint: "spain:7002", Owner: "user:o", At: 1}
	want := "figaro troupe entry v1\n" + `{"kind":"join","troupe":"estate","node":"n1","door":"spain:7002","owner":"user:o","at":1}`
	require.Equal(t, want, string(e.message()))
}

func TestAJoinStoredBefore069KeepsItsEndpoint(t *testing.T) {
	var e Entry
	require.NoError(t, json.Unmarshal([]byte(`{"kind":"join","troupe":"estate","node":"n1","door":"spain:7002","at":1,"sig":"AA=="}`), &e))
	require.Equal(t, "spain:7002", e.Endpoint)
	raw, err := json.Marshal(e)
	require.NoError(t, err)
	require.Contains(t, string(raw), `"door":"spain:7002"`)
}
