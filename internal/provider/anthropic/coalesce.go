package anthropic

import (
	"encoding/json"
)

// coalesceRows merges adjacent same-role rows by concatenating their content,
// so the request alternates roles as the API requires.
//
// THE LOG IS NOT TOUCHED. Two consecutive user records are a fact about the
// history -- a turn that errored after committing the prompt and before any
// reply -- and the translator log is append-only, so the repair belongs where
// the request is assembled and nowhere else. The SDK provider has always done
// this over its typed params; this is the same repair over stored bytes.
//
// It decodes ONLY the rows it merges. Adjacent same-role pairs are rare, so
// the common case pays one role peek per row and no allocation.
func coalesceRows(rows []json.RawMessage, lts []uint64) ([]json.RawMessage, []uint64) {
	if len(rows) < 2 {
		return rows, lts
	}
	roles := make([]string, len(rows))
	need := false
	for i, r := range rows {
		roles[i] = rowRole(r)
		if i > 0 && roles[i] != "" && roles[i] == roles[i-1] {
			need = true
		}
	}
	if !need {
		return rows, lts
	}

	outRows := make([]json.RawMessage, 0, len(rows))
	outLTs := make([]uint64, 0, len(lts))
	ltAt := func(i int) uint64 {
		if i < len(lts) {
			return lts[i]
		}
		return 0
	}
	for i, r := range rows {
		n := len(outRows) - 1
		if n >= 0 && roles[i] != "" && roles[i] == rowRole(outRows[n]) {
			if merged, ok := mergeRows(outRows[n], r); ok {
				outRows[n] = merged
				// The later LT wins: it is the one a per-LT tag would target,
				// which is what the SDK path does with the same choice.
				if len(outLTs) > 0 {
					outLTs[len(outLTs)-1] = ltAt(i)
				}
				continue
			}
		}
		outRows = append(outRows, r)
		outLTs = append(outLTs, ltAt(i))
	}
	return outRows, outLTs
}

// rowRole peeks a row's role without decoding its content.
func rowRole(raw json.RawMessage) string {
	var m struct {
		Role string `json:"role"`
	}
	if json.Unmarshal(raw, &m) != nil {
		return ""
	}
	return m.Role
}

// mergeRows concatenates b's content onto a's. It reports false when either
// row cannot be read as a message, in which case the caller leaves both alone:
// a row we cannot parse is a row we must not rewrite.
func mergeRows(a, b json.RawMessage) (json.RawMessage, bool) {
	var ma, mb nativeMessage
	if json.Unmarshal(a, &ma) != nil || json.Unmarshal(b, &mb) != nil {
		return nil, false
	}
	ma.Content = append(ma.Content, mb.Content...)
	merged, err := json.Marshal(ma)
	if err != nil {
		return nil, false
	}
	return merged, true
}
