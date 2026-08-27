package anthropic

import (
	"bytes"
	"encoding/json"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/internal/provider"
)

// THE ORACLE. Every function below is a VERBATIM copy of the slice assembler
// as it stood at 6a650ef8, renamed. It is kept so the streamed assembler can
// be held to the only standard that matters for invariant #5 -- the prefix
// bytes a provider caches on -- which is that the bytes did not move.
//
// Delete it when the sequence has outlived the doubt, not before.

func (a *Anthropic) legacyProject(perMessage [][]json.RawMessage, lts []uint64, snapshot form.Snapshot, tools []provider.Tool, maxTokens int, oauth bool, model string) (nativeRequest, error) {
	if maxTokens == 0 {
		maxTokens = a.MaxTokens
	}
	if maxTokens == 0 {
		maxTokens = 8192
	}
	req := nativeRequest{
		Model: model, MaxTokens: maxTokens, Stream: true,
		System: systemBlocks(snapshot, oauth),
		// TODO: put tools on the form as an ordered list.
		Tools: projectTools(tools),
	}
	var msgLTs []uint64
	for i, entry := range perMessage {
		var lt uint64
		if i < len(lts) {
			lt = lts[i]
		}
		for _, raw := range entry {
			if len(raw) == 0 {
				continue
			}
			req.Messages = append(req.Messages, raw)
			msgLTs = append(msgLTs, lt)
		}
	}

	// BEFORE ANY MARKING: cache breakpoints and per-LT tags address messages by
	// INDEX, so merging after them would move the marks onto other messages.
	req.Messages = legacyDropDuplicateResults(req.Messages)
	req.Messages, msgLTs = legacyCoalesceRows(req.Messages, msgLTs)

	if policy := provider.ResolveCachePolicy(snapshot); !policy.Off() {
		route := a.route()
		// Deliberately NOT gated on the model's minimum cacheable size.
		// Figaro spends 3 of 4 slots, so there is no scarcity to protect,
		// and a marker below the minimum is ignored rather than charged.
		// Gating would trade a free no-op for a false negative on any
		// prompt a chars/4 estimate under-counts. The thresholds live in
		// provider.CacheMinTokens for an endpoint that DOES have to ration
		// slots and knows which model it resolved.
		if plan := route.MarkPlan(provider.ResolveMarkMode(snapshot)); plan.Blocks {
			legacyMarkCacheBreakpoints(&req, policy, plan, route.Caps)
		}
	}
	legacyApplyMessageTags(&req, msgLTs, snapshot, a.route().Caps)
	applyThinking(&req, snapshot, model)
	return req, nil
}

// legacyApplyMessageTags reads system.tags and applies per-message
// cache_control overrides keyed by logical time.
func legacyApplyMessageTags(req *nativeRequest, msgLTs []uint64, snapshot form.Snapshot, caps provider.CacheCaps) {
	raw, ok := snapshot.Get("system.tags")
	if !ok || len(raw) == 0 {
		return
	}
	var tags map[string]struct {
		CacheControl string `json:"cache_control"`
	}
	if err := json.Unmarshal(raw, &tags); err != nil {
		return
	}
	if len(tags) == 0 {
		return
	}

	lastIdx := make(map[uint64]int, len(msgLTs))
	for i, lt := range msgLTs {
		if lt == 0 {
			continue
		}
		lastIdx[lt] = i
	}
	for key, tag := range tags {
		if tag.CacheControl == "" {
			continue
		}
		lt, err := strconv.ParseUint(key, 10, 64)
		if err != nil {
			continue
		}
		idx, ok := lastIdx[lt]
		if !ok {
			continue
		}
		policy := provider.ParseCachePolicy(tag.CacheControl)
		if policy.Off() {
			continue
		}
		markRowTail(&req.Messages[idx], controlFor(policy, caps))
	}
}

func legacyMarkCacheBreakpoints(req *nativeRequest, policy provider.CachePolicy, plan provider.MarkPlan, caps provider.CacheCaps) {
	cc := controlFor(policy, caps)
	budget := caps.MaxMarkers
	if budget > provider.AutoCacheBreakpoints {
		budget = provider.AutoCacheBreakpoints
	}
	spend := func() bool {
		if budget <= 0 {
			return false
		}
		budget--
		return true
	}
	if n := len(req.System); n > 0 && spend() {
		req.System[n-1].CacheControl = cc
	}
	if n := len(req.Tools); n > 0 && spend() {
		req.Tools[n-1].CacheControl = cc
	}
	if !plan.Tail {
		return
	}
	if n := len(req.Messages); n >= 1 && spend() {
		markRowTail(&req.Messages[n-1], cc)
	}
}

// legacyDropDuplicateResults removes a tool_result block whose call an EARLIER block
// already answered. The wire pairs one result with one invoke, so a second is
// refused with "unexpected tool_use_id found in tool_result blocks" and the
// whole history becomes unsendable.
//
// The fig IR is append-only and keeps both records, which is honest: two
// closings really were written. This is the last gate before the wire, and
// unmatched on the wire is a hard error -- the same rule the door applies to a
// late result. A row with no duplicate is returned untouched, bytes and all.
func legacyDropDuplicateResults(rows []json.RawMessage) []json.RawMessage {
	seen := map[string]bool{}
	out := rows
	rewritten := false
	for i, raw := range rows {
		var m nativeMessage
		if json.Unmarshal(raw, &m) != nil {
			continue // a row we cannot read is a row we must not rewrite
		}
		keep := make([]nativeBlock, 0, len(m.Content))
		dropped := false
		for _, b := range m.Content {
			if b.Type == "tool_result" && b.ToolUseID != "" {
				if seen[b.ToolUseID] {
					dropped = true
					continue
				}
				seen[b.ToolUseID] = true
			}
			keep = append(keep, b)
		}
		if !dropped {
			continue
		}
		if !rewritten {
			out = append([]json.RawMessage(nil), rows...)
			rewritten = true
		}
		if len(keep) == 0 {
			out[i] = nil // an empty message is skipped by the splice
			continue
		}
		m.Content = keep
		if fixed, err := json.Marshal(m); err == nil {
			out[i] = fixed
		}
	}
	if !rewritten {
		return rows
	}
	compact := out[:0]
	for _, raw := range out {
		if len(raw) > 0 {
			compact = append(compact, raw)
		}
	}
	return compact
}

// legacyCoalesceRows merges adjacent same-role rows by concatenating their content,
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
func legacyCoalesceRows(rows []json.RawMessage, lts []uint64) ([]json.RawMessage, []uint64) {
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

// --- the equality ---------------------------------------------------------

func legacyBody(t *testing.T, a *Anthropic, perMessage [][]json.RawMessage, lts []uint64,
	snap form.Snapshot, tools []provider.Tool, model string) []byte {
	t.Helper()
	req, err := a.legacyProject(perMessage, lts, snap, tools, 4096, false, model)
	require.NoError(t, err)
	var buf bytes.Buffer
	require.NoError(t, bodyFunc(req)(&buf))
	return buf.Bytes()
}

func streamedBody(t *testing.T, a *Anthropic, perMessage [][]json.RawMessage, lts []uint64,
	snap form.Snapshot, tools []provider.Tool, model string) []byte {
	t.Helper()
	req, rows, err := a.projectRequest(provider.PerMessageRows(perMessage, lts), snap, tools, 4096, false, model)
	require.NoError(t, err)
	var buf bytes.Buffer
	require.NoError(t, bodyFuncSeq(req, rows)(&buf))
	return buf.Bytes()
}

func raw(s string) json.RawMessage { return json.RawMessage(s) }

func msg(role, text string) json.RawMessage {
	return raw(`{"role":"` + role + `","content":[{"type":"text","text":"` + text + `"}]}`)
}

func toolUse(id string) json.RawMessage {
	return raw(`{"role":"assistant","content":[{"type":"tool_use","id":"` + id + `","name":"read","input":{}}]}`)
}

func toolResult(id, text string) json.RawMessage {
	return raw(`{"role":"user","content":[{"type":"tool_result","tool_use_id":"` + id + `","content":"` + text + `"}]}`)
}

// THE BYTES DID NOT MOVE. The streamed assembler is held to the deleted one
// over every shape the passes react to: plain rows, adjacent same-role rows
// that coalesce, a duplicate tool_result that is rewritten out of its message,
// per-LT tags, and the rolling tail marker.
func TestStreamedAssemblyIsByteIdenticalToTheSliceAssembler(t *testing.T) {
	cases := []struct {
		name       string
		perMessage [][]json.RawMessage
		lts        []uint64
		board      map[string]json.RawMessage
	}{
		{
			name:       "plain",
			perMessage: [][]json.RawMessage{{msg("user", "hi")}, {msg("assistant", "hello")}},
			lts:        []uint64{1, 2},
		},
		{
			name: "adjacent same role coalesces",
			perMessage: [][]json.RawMessage{
				{msg("user", "one"), msg("user", "two")},
				{msg("assistant", "three")},
				{msg("assistant", "four")},
			},
			lts: []uint64{1, 2, 3},
		},
		{
			name: "duplicate tool_result is rewritten",
			perMessage: [][]json.RawMessage{
				{toolUse("call_1")},
				{raw(`{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_1","content":"a"},{"type":"text","text":"and"}]}`)},
				{raw(`{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_1","content":"b"},{"type":"text","text":"again"}]}`)},
			},
			lts: []uint64{1, 2, 3},
		},
		{
			name: "per-LT tags mark the last row of their time",
			perMessage: [][]json.RawMessage{
				{msg("user", "one")},
				{msg("assistant", "two"), msg("assistant", "three")},
				{msg("user", "four")},
			},
			lts:   []uint64{1, 2, 3},
			board: map[string]json.RawMessage{"system.tags": raw(`{"2":{"cache_control":"ephemeral"}}`)},
		},
		{
			// THE TWO MARKS COLLIDE HERE, and their order decides the bytes:
			// the rolling tail breakpoint is spent first and a per-LT tag on
			// the same row overwrites it. A fixture whose tag sits anywhere
			// but the last row cannot see the difference -- one did not, and
			// a swapped order survived it.
			name: "a tag on the LAST row overwrites the tail marker",
			perMessage: [][]json.RawMessage{
				{msg("user", "one")},
				{msg("assistant", "two")},
				{msg("user", "three")},
			},
			lts:   []uint64{1, 2, 3},
			board: map[string]json.RawMessage{"system.tags": raw(`{"3":{"cache_control":"long"}}`)},
		},
		{
			name:       "empty rows are skipped",
			perMessage: [][]json.RawMessage{{raw("")}, {msg("user", "only")}, {}},
			lts:        []uint64{1, 2, 3},
		},
		{
			name: "a tool result whose call is unmatched is left alone",
			perMessage: [][]json.RawMessage{
				{toolResult("call_x", "orphan")},
				{msg("assistant", "ok")},
			},
			lts: []uint64{1, 2},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			board := map[string]json.RawMessage{"system.credo": raw(`"you are a test agent"`)}
			for k, v := range tc.board {
				board[k] = v
			}
			snap := form.FromMap(board)
			a := &Anthropic{Model: "claude-opus-5", ReminderRenderer: "tag"}
			want := legacyBody(t, a, tc.perMessage, tc.lts, snap, nil, "claude-opus-5")
			got := streamedBody(t, a, tc.perMessage, tc.lts, snap, nil, "claude-opus-5")
			require.Equal(t, string(want), string(got))
		})
	}
}

// A row emptied by the duplicate pass takes its logical time with it.
//
// THE SLICE VERSION DID NOT. It compacted the rows and left the LT array at
// its old length, so after a drop every per-LT tag addressed the row one place
// to its left -- silently, on the only pass that can move a cache breakpoint.
// This is the one case where the streamed assembler is DELIBERATELY not
// byte-identical to the oracle, and the oracle is the one that is wrong.
func TestADroppedRowTakesItsLogicalTimeWithIt(t *testing.T) {
	perMessage := [][]json.RawMessage{
		{toolUse("call_1")},
		{toolResult("call_1", "first")},
		{toolResult("call_1", "duplicate, whole row goes")},
		{msg("user", "after")},
	}
	lts := []uint64{1, 2, 3, 4}
	snap := form.FromMap(map[string]json.RawMessage{
		"system.credo": raw(`"you are a test agent"`),
		"system.tags":  raw(`{"4":{"cache_control":"ephemeral"}}`),
	})
	a := &Anthropic{Model: "claude-opus-5", ReminderRenderer: "tag"}

	rows, gotLTs := provider.CollectRows(coalesceRowsSeq(dropDuplicateResultsSeq(
		provider.PerMessageRows(perMessage, lts))))
	require.Equal(t, len(rows), len(gotLTs), "a pair cannot desynchronize")
	require.Equal(t, uint64(4), gotLTs[len(gotLTs)-1], "the last row still carries LT 4")

	// And the tag lands on the row that carries the time it names.
	req, seq, err := a.projectRequest(provider.PerMessageRows(perMessage, lts), snap, nil, 4096, false, "claude-opus-5")
	require.NoError(t, err)
	marked, _ := provider.CollectRows(seq)
	req.Messages = marked
	require.Contains(t, string(marked[len(marked)-1]), "cache_control")
	require.LessOrEqual(t, countCacheMarkers(req), provider.MaxCacheBreakpoints)
}
