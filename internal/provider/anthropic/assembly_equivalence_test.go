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

// The legacy* functions are the slice assembler, kept verbatim as the byte
// oracle for the streamed one (invariant #5).

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

	req.Messages = legacyDropDuplicateResults(req.Messages)
	req.Messages, msgLTs = legacyCoalesceRows(req.Messages, msgLTs)

	if policy := provider.ResolveCachePolicy(snapshot); !policy.Off() {
		route := a.route()
		if plan := route.MarkPlan(provider.ResolveMarkMode(snapshot)); plan.Blocks {
			legacyMarkCacheBreakpoints(&req, policy, plan, route.Caps)
		}
	}
	legacyApplyMessageTags(&req, msgLTs, snapshot, a.route().Caps)
	applyThinking(&req, snapshot, model)
	return req, nil
}

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
			// The tag must sit on the last row to exercise the mark order.
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

// The one deliberate divergence from the oracle: an emptied row takes its
// LT with it, where the oracle left the LT array unshifted.
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

	req, seq, err := a.projectRequest(provider.PerMessageRows(perMessage, lts), snap, nil, 4096, false, "claude-opus-5")
	require.NoError(t, err)
	marked, _ := provider.CollectRows(seq)
	req.Messages = marked
	require.Contains(t, string(marked[len(marked)-1]), "cache_control")
	require.LessOrEqual(t, countCacheMarkers(req), provider.MaxCacheBreakpoints)
}
