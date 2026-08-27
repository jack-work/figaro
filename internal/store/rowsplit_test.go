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
			// ONE conversion, used for both. []byte(s) allocates a fresh
			// array every time it is written, so converting twice compares
			// two unrelated buffers -- which reported copies that were not
			// copies, and could just as easily have reported an alias that
			// was not one, if the two happened to land adjacent.
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

// THE RULE, ENFORCED: alias on the way to the wire, copy on the way into the
// cache.
//
// A cached entry is sized as the sum of its rows' lengths. If those rows were
// windows onto a bigger frame, the entry would report less than it holds and
// the budget would stop bounding what it believes it bounds -- the same
// failure eeec2bc0 was written about, one layer down. So a row that reaches
// the cache must own its bytes, and this test reaches into the cache to check.
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

	// A COLD BACKEND, OR THIS TEST PROVES NOTHING. An append seeds the tree
	// cache with the payload the WRITER passed -- never a decoded frame -- so
	// a read on the same process is served from memory and the decode path
	// under test never runs. It did not, and the test passed anyway against a
	// deliberately broken decoder. Reopening is what makes the read cold.
	require.NoError(t, b.Close())
	cold, err := NewXwalBackend(root, 0)
	require.NoError(t, err)
	defer cold.Close()
	coldRows, err := cold.OpenTranslator(aria, "anthropic")
	require.NoError(t, err)

	read := coldRows.Read()
	require.Len(t, read, 8)

	// TWO ROWS OF ONE RECORD ARE THE TEST. In the frame they are separated
	// by exactly one comma, so a WINDOW onto that frame puts row 1 at
	// &row0 + len(row0) + 1. A copy cannot land there except by an accident
	// no allocator will hand you twice at this size.
	//
	// (cap(row) != len(row) is NOT the test: json.RawMessage decodes with
	// append, which rounds up to a size class, so an owned 4124-byte row has
	// capacity 4864 and the naive check calls it a window.)
	for i, e := range read {
		require.Len(t, e.Payload, 2)
		first, second := e.Payload[0], e.Payload[1]
		gap := uintptr(unsafe.Pointer(&second[0])) - uintptr(unsafe.Pointer(&first[0]))
		require.NotEqual(t, uintptr(len(first)+1), gap,
			"entry %d: the two rows are contiguous in one frame, so the cache holds windows", i)
	}
}
