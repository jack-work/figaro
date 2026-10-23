package form_test

import (
	"encoding/json"

	"github.com/jack-work/figaro/api/form"
)

// Every Snapshot construction and read in the benchmarks goes through this
// file.

func buildBoundForm(m map[string]json.RawMessage) form.Snapshot {
	return form.FromMap(m)
}

func boundFormGet(s form.Snapshot, key string) (json.RawMessage, bool) {
	return s.Get(key)
}

func boundFormLen(s form.Snapshot) int {
	return s.Len()
}

func unmarshalBoundForm(data []byte) (form.Snapshot, error) {
	var s form.Snapshot
	err := json.Unmarshal(data, &s)
	return s, err
}
