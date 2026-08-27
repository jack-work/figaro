package anthropic

import (
	"encoding/json"
)

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

// resultsFirst hoists tool_result blocks to the front of a user turn, keeping
// the order within each group. STABLE, so a result set stays in call order and
// the prose after it stays in the order it was written.
//
// THE PAIRING RULE IS POSITIONAL. The API does not merely want the results in
// the message after the invoke, it wants them AT ITS HEAD: a text block in
// front of them and the whole request is refused with "tool_use ids were found
// without tool_result blocks immediately after".
//
// Every way that block gets in front is legitimate, which is why the repair
// lives here and not at any one of them:
//
//   - A FORK TAKEN MID-CALL. The branch's fork notice is a record of its own,
//     landing between the invoke and the results the parent's tools were still
//     producing. Coalescing then merges the notice and the results into one
//     user turn, notice first. This bricked an aria for good: the malformed
//     pair replays on every later request (Gluck, 0.29.5, aria 45c24dda).
//   - THE DOOR'S OWN CLOSING RESULTS, which it appends to the message it is
//     given, so a message that opens with prose keeps its prose in front.
//   - PATCHES, STUDY AND FORK REMINDERS, which render after the content they
//     ride along with.
//
// The model reads the same blocks either way. The API only accepts one order.
func resultsFirst(blocks []nativeBlock) []nativeBlock {
	first := -1
	for i, b := range blocks {
		if b.Type == "tool_result" {
			first = i
			break
		}
	}
	// Nothing to hoist, or the results already lead: the common path decides
	// with one scan and no allocation.
	if first <= 0 {
		return blocks
	}
	out := make([]nativeBlock, 0, len(blocks))
	for _, b := range blocks {
		if b.Type == "tool_result" {
			out = append(out, b)
		}
	}
	for _, b := range blocks {
		if b.Type != "tool_result" {
			out = append(out, b)
		}
	}
	return out
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
	if ma.Role == "user" {
		// The merge is where the fork notice gets in front of the results.
		ma.Content = resultsFirst(ma.Content)
	}
	merged, err := json.Marshal(ma)
	if err != nil {
		return nil, false
	}
	return merged, true
}
