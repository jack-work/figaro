package form_test

import (
	"encoding/json"

	"github.com/jack-work/figaro/api/form"
)

// Every Snapshot construction and read in the benchmarks goes through this
// file.

func buildBoard(m map[string]json.RawMessage) form.Snapshot {
	return form.FromMap(m)
}

func boardGet(s form.Snapshot, key string) (json.RawMessage, bool) {
	return s.Get(key)
}

func boardLen(s form.Snapshot) int {
	return s.Len()
}

func unmarshalBoard(data []byte) (form.Snapshot, error) {
	var s form.Snapshot
	err := json.Unmarshal(data, &s)
	return s, err
}
