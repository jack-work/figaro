package store

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"unsafe"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/internal/store/xwal"
)

// aliases reports whether row is a window onto payload rather than a copy.
// It compares the ADDRESSES of the backing arrays, which is the only honest
// way to ask: equal bytes prove nothing.
func aliases(payload []byte, row []byte) bool {
	if len(payload) == 0 || len(row) == 0 {
		return false
	}
	base := uintptr(unsafe.Pointer(&payload[0]))
	at := uintptr(unsafe.Pointer(&row[0]))
	return at >= base && at < base+uintptr(len(payload))
}

// The splitter must agree with encoding/json on every payload the store can
// hold, and disagree with it about nothing but where the bytes live.
func TestSplitJSONArrayMatchesEncodingJSON(t *testing.T) {
	cases := []string{
		`[]`,
		`[{"role":"user"}]`,
		`[{"role":"user"},{"role":"assistant"}]`,
		`[{"a":[1,2,{"b":"]"}]},{"c":"}"}]`,
		`[{"text":"a \" quote and a \\\\ backslash"}]`,
		`[{"text":"bracket ] inside a string"},{"text":"brace } inside"}]`,
		`[{"text":"\u00e9\u005d"}]`,
		`  [ {"a":1} , {"b":2} ]  `,
		`[1,2,3]`,
		`[true,false,null]`,
		`[{"nested":{"deep":{"deeper":[{"x":1}]}}}]`,
		`[{"text":"newline\nand\ttab"}]`,
		`[""]`,
		`["]","[",","]`,
	}
	for _, in := range cases {
		t.Run(in, func(t *testing.T) {
			// One conversion for both: []byte(s) allocates a fresh array each
			// time, so converting twice compares two unrelated buffers.
			payload := []byte(in)
			var want []json.RawMessage
			require.NoError(t, json.Unmarshal(payload, &want), "fixture is not valid JSON")

			got, ok := splitJSONArray(payload)
			require.True(t, ok, "splitter refused a payload encoding/json accepted")
			require.Equal(t, len(want), len(got))
			for i := range want {
				require.JSONEq(t, string(want[i]), string(got[i]), "element %d", i)
			}
			if len(got) > 0 && len(got[0]) > 0 {
				require.True(t, aliases(payload, got[0]),
					"the split element is a copy: the whole point is that it is not")
			}
		})
	}
}

// Anything that is not an array of ours falls back to encoding/json rather
// than being guessed at.
func TestSplitJSONArrayRefusesWhatItCannotRead(t *testing.T) {
	for _, in := range []string{
		`null`, `{"not":"an array"}`, `[`, `[{"a":1}`, `[{"a":1},]`, `[,]`,
		`[{"a":1}] trailing`, ``, `"a string"`, `[{"a":1}][{"b":2}]`,
	} {
		t.Run(in, func(t *testing.T) {
			_, ok := splitJSONArray([]byte(in))
			require.False(t, ok, "the splitter accepted something it should have refused")
		})
	}
}

// A row on its way to the wire is a window onto the frame.
func TestScannedRowsAliasTheirFrame(t *testing.T) {
	payload := []byte(`[{"role":"user","content":"hello"},{"role":"assistant","content":"hi"}]`)
	rec := xwal.Record{ChannelLT: 1, MainLT: 1, Payload: payload}

	aliased, ok := decodeRecordAliased[[]json.RawMessage](rec)
	require.True(t, ok)
	require.Len(t, aliased.Payload, 2)
	for i, row := range aliased.Payload {
		require.True(t, aliases(payload, row), "row %d was copied", i)
	}

	owned, ok := decodeRecord[[]json.RawMessage](rec)
	require.True(t, ok)
	require.Len(t, owned.Payload, 2)
	for i, row := range owned.Payload {
		require.False(t, aliases(payload, row), "row %d aliases the frame it must own", i)
		require.JSONEq(t, string(aliased.Payload[i]), string(row))
	}
}

// The rule, enforced: alias on the way to the wire, copy on the way into the
// cache. A cached entry is sized as the sum of its rows' lengths, so a row
// that reaches the cache must own its bytes.
func TestCacheSeededRowsDoNotAliasTheirFrame(t *testing.T) {
	root := t.TempDir()
	b, err := NewXwalBackend(root, 0)
	require.NoError(t, err)

	outfit, err := b.CreateOutfit("l", setPatch(map[string]string{"system.model": "m"}))
	require.NoError(t, err)
	aria, _, err := b.ForkWith(outfit, 0, setPatch(map[string]string{"system.cwd": "/tmp"}))
	require.NoError(t, err)
	rows, err := b.OpenTranslator(aria, "anthropic")
	require.NoError(t, err)

	big := strings.Repeat("x", 4096)
	for i := 0; i < 8; i++ {
		_, err := rows.Append(Entry[[]json.RawMessage]{
			FigaroLT: uint64(i + 1),
			Payload: []json.RawMessage{
				json.RawMessage(fmt.Sprintf(`{"role":"user","content":%q}`, big)),
				json.RawMessage(fmt.Sprintf(`{"role":"assistant","content":%q}`, big)),
			},
		})
		require.NoError(t, err)
	}

	// A cold backend, or this test proves nothing: an append seeds the tree
	// cache with the payload the writer passed, so a warm read never runs
	// the decode path under test.
	require.NoError(t, b.Close())
	cold, err := NewXwalBackend(root, 0)
	require.NoError(t, err)
	defer cold.Close()
	coldRows, err := cold.OpenTranslator(aria, "anthropic")
	require.NoError(t, err)

	read := coldRows.Read()
	require.Len(t, read, 8)

	// Two rows of one record are the test: in the frame they are separated by
	// one comma, so a window puts row 1 at &row0 + len(row0) + 1, where a copy
	// cannot land. (cap != len is not the test: append rounds up to a size class.)
	for i, e := range read {
		require.Len(t, e.Payload, 2)
		first, second := e.Payload[0], e.Payload[1]
		gap := uintptr(unsafe.Pointer(&second[0])) - uintptr(unsafe.Pointer(&first[0]))
		require.NotEqual(t, uintptr(len(first)+1), gap,
			"entry %d: the two rows are contiguous in one frame, so the cache holds windows", i)
	}
}
