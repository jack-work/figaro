package store

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
	"github.com/stretchr/testify/require"
)

// Rewind the records, not just schema.json: a current patch under an old
// version stamp never exercises conversion of inherited removals.
func rewindChannels(t *testing.T, root string) {
	t.Helper()
	for _, channel := range []string{chanForm, chanIR} {
		nodes, order, err := loadMigrateNodes(filepath.Join(root, channel))
		require.NoError(t, err)
		converted := 0
		for _, name := range order {
			segs, err := migrateSegments(nodes[name].dir)
			require.NoError(t, err)
			for _, sg := range segs {
				payloads, err := readMigrateSegment(sg)
				require.NoError(t, err)
				for i, raw := range payloads {
					frame, payload, ok := framePayload(raw)
					if !ok {
						continue
					}
					if channel == chanForm {
						var p message.Patch
						require.NoError(t, json.Unmarshal(payload, &p))
						flat := flatPatch{Set: map[string]json.RawMessage{}}
						for _, e := range p.Entries() {
							if len(e.New) == 0 {
								flat.Remove = append(flat.Remove, e.Key)
							} else {
								flat.Set[e.Key] = e.New
							}
						}
						frame["p"], err = json.Marshal(flat)
						require.NoError(t, err)
						_, old := readFlatPatch(frame["p"])
						require.True(t, old, "fixture is not a flat patch")
					} else {
						var obj map[string]json.RawMessage
						require.NoError(t, json.Unmarshal(payload, &obj))
						delete(obj, "turn_id")
						frame["p"], err = json.Marshal(obj)
						require.NoError(t, err)
					}
					payloads[i], err = json.Marshal(frame)
					require.NoError(t, err)
					converted++
				}
				require.NoError(t, rewriteMigrateSegment(sg, payloads))
			}
		}
		require.Positive(t, converted, "fixture never rewound %s records", channel)
	}
	schema, err := readSchema(root)
	require.NoError(t, err)
	schema.Channels[chanForm] = 1
	schema.Channels[chanIR] = 4
	require.NoError(t, writeSchema(root, schema))
}

func TestMigrationsPreserveInheritedRemovalAndTurnIDs(t *testing.T) {
	root := t.TempDir()
	be, err := NewXwalBackend(root, 0)
	require.NoError(t, err)
	outfit, err := be.CreateOutfit("migration-lineage", setPatch(map[string]string{
		"obsolete": "inherited", "nested.keep": "yes", "nested.change": "old",
	}))
	require.NoError(t, err)
	aria, err := be.CreateConversation(outfit)
	require.NoError(t, err)
	before, err := be.FormState(aria)
	require.NoError(t, err)
	_, err = be.ApplyForm(aria, form.Build(before, map[string]json.RawMessage{
		"nested.change": json.RawMessage(`"new"`),
	}, []string{"obsolete"}))
	require.NoError(t, err)
	lg, err := be.OpenFigIR(aria)
	require.NoError(t, err)
	for _, m := range []message.Message{prompt("one"), reply("answer one"), prompt("two"), reply("answer two")} {
		_, err = lg.Append(Entry[message.Message]{Payload: m})
		require.NoError(t, err)
	}
	_, alt, err := be.ForkAt(aria, lg.Read()[1].LT)
	require.NoError(t, err)
	altLog, err := be.OpenFigIR(alt)
	require.NoError(t, err)
	_, err = altLog.Append(Entry[message.Message]{Payload: prompt("alternative")})
	require.NoError(t, err)

	boards := map[string]string{}
	turnIDs := map[string][]uint64{}
	for _, id := range []string{aria, alt} {
		boards[id] = boardOf(t, be, id)
		log, err := be.OpenFigIR(id)
		require.NoError(t, err)
		for _, e := range log.Read() {
			turnIDs[id] = append(turnIDs[id], e.Payload.TurnID)
		}
	}
	require.NoError(t, be.Close())
	rewindChannels(t, root)

	be, err = NewXwalBackend(root, 0)
	require.NoError(t, err)
	for _, id := range []string{aria, alt} {
		var prior form.Snapshot
		require.NoError(t, (&xwalFormLog{backend: be, ariaID: id}).RangePatches(0, 0, func(index uint64, raw []byte) error {
			var p message.Patch
			require.NoError(t, json.Unmarshal(raw, &p))
			next := prior.Apply(p)
			want, err := json.Marshal(prior)
			require.NoError(t, err)
			got, err := json.Marshal(next.Apply(p.Inverse()))
			require.NoError(t, err)
			require.JSONEq(t, string(want), string(got), "patch %d lost its inherited before value", index)
			prior = next
			return nil
		}))
		require.Equal(t, boards[id], boardOf(t, be, id), "migrated board %s", id)
		log, err := be.OpenFigIR(id)
		require.NoError(t, err)
		var got []uint64
		for _, e := range log.Read() {
			got = append(got, e.Payload.TurnID)
		}
		require.Equal(t, turnIDs[id], got, "migrated turn IDs %s", id)
	}
	require.NoError(t, be.Close())
	first := snapshotTree(t, root)
	be, err = NewXwalBackend(root, 0)
	require.NoError(t, err)
	require.NoError(t, be.Close())
	require.Equal(t, first, snapshotTree(t, root), "reopen rewrote migrated records")
}
