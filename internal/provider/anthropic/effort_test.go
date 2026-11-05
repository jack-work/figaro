package anthropic

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
)

func effortConversation(t *testing.T, a *Anthropic) ([][]json.RawMessage, form.Snapshot) {
	t.Helper()
	set := func(effort string) []message.Patch {
		return []message.Patch{form.Build(form.Snapshot{}, map[string]json.RawMessage{
			"system.thinking_effort": json.RawMessage(`"` + effort + `"`),
		}, nil)}
	}
	msgs := []message.Message{
		{Role: message.RoleInput, LogicalTime: 1, Patches: set("high"),
			Content: []message.Content{message.TextContent("prove it")}},
		{Role: message.RoleOutput, LogicalTime: 2, StopReason: message.StopEnd,
			Content: []message.Content{message.TextContent("done")}},
		{Role: message.RoleInput, LogicalTime: 3, Patches: set("low"),
			Content: []message.Content{message.TextContent("thanks")}},
	}
	var rows [][]json.RawMessage
	snap := form.Snapshot{}
	for _, m := range msgs {
		r, err := a.encode(m, snap)
		if err != nil {
			t.Fatal(err)
		}
		snap = form.Fold(snap, m.Patches)
		rows = append(rows, r)
	}
	return rows, snap
}

func roles(t *testing.T, req nativeRequest) string {
	t.Helper()
	var out []string
	for _, raw := range req.Messages {
		out = append(out, rowRole(raw))
	}
	return strings.Join(out, ",")
}

// Each inquiry carries the effort in force for it, so a change appends a row
// instead of rewriting the request's top level, which Anthropic renders ahead
// of the conversation and so invalidates every cached message after it.
func TestAPerMessageEffortModelCarriesEffortInBand(t *testing.T) {
	a := &Anthropic{MaxTokens: 64}
	rows, snap := effortConversation(t, a)
	req, err := a.projectMessagesWithModel(rows, snap, nil, 64, false, "claude-opus-5-5")
	if err != nil {
		t.Fatal(err)
	}
	if got := roles(t, req); got != "system,user,assistant,system,user" {
		t.Fatalf("roles = %s", got)
	}
	if !strings.Contains(string(req.Messages[0]), `"effort":"high"`) || !strings.Contains(string(req.Messages[3]), `"effort":"low"`) {
		t.Fatalf("effort rows = %s / %s", req.Messages[0], req.Messages[3])
	}
	if req.OutputConfig != nil {
		t.Fatalf("top-level effort %+v would change between requests and break the cache", req.OutputConfig)
	}
	if req.Thinking == nil || req.Thinking.Type != "adaptive" {
		t.Fatalf("thinking = %+v", req.Thinking)
	}
	if got := messageBetas("claude-opus-5-5"); len(got) != 1 || got[0] != betaMessageEffort {
		t.Fatalf("betas = %v", got)
	}
}

func TestAnOlderModelGetsTheEffortAtTheTopLevel(t *testing.T) {
	a := &Anthropic{MaxTokens: 64}
	rows, snap := effortConversation(t, a)
	req, err := a.projectMessagesWithModel(rows, snap, nil, 64, false, "claude-opus-4-7")
	if err != nil {
		t.Fatal(err)
	}
	if got := roles(t, req); got != "user,assistant,user" {
		t.Fatalf("roles = %s: a model without per-message effort was sent effort rows", got)
	}
	if req.OutputConfig == nil || req.OutputConfig.Effort != "low" {
		t.Fatalf("output_config = %+v", req.OutputConfig)
	}
	if got := messageBetas("claude-opus-4-7"); len(got) != 0 {
		t.Fatalf("betas = %v", got)
	}
}

func TestOnlyEffortOnAPerMessageModelKeepsTheCache(t *testing.T) {
	bound := func(model string) form.Snapshot {
		return form.Snapshot{}.Apply(form.Build(form.Snapshot{}, map[string]json.RawMessage{
			"system.model": json.RawMessage(`"` + model + `"`),
		}, nil))
	}
	a := &Anthropic{}
	if !a.KeepsCacheOnChange("system.thinking_effort", bound("claude-opus-5.5")) {
		t.Fatal("copilot's spelling of opus 5.5 supports per-message effort")
	}
	if a.KeepsCacheOnChange("system.thinking_effort", bound("claude-opus-4-7")) {
		t.Fatal("opus 4.7 has no per-message effort")
	}
	if a.KeepsCacheOnChange("system.model", bound("claude-opus-5-5")) {
		t.Fatal("a model change never keeps the cache")
	}
}

func effortPatch(effort string) []message.Patch {
	if effort == "" {
		return []message.Patch{form.Build(form.Snapshot{}, nil, []string{"system.thinking_effort"})}
	}
	return []message.Patch{form.Build(form.Snapshot{}, map[string]json.RawMessage{
		"system.thinking_effort": json.RawMessage(`"` + effort + `"`),
	}, nil)}
}

func toolTurn(midTurn []message.Patch) []message.Message {
	return []message.Message{
		{Role: message.RoleInput, LogicalTime: 1, Patches: effortPatch("high"),
			Content: []message.Content{message.TextContent("look around")}},
		{Role: message.RoleOutput, LogicalTime: 2, StopReason: message.StopToolInvoke,
			Content: []message.Content{{Type: message.ContentToolInvoke, ToolCallID: "toolu_1", ToolName: "bash"}}},
		{Role: message.RoleInput, LogicalTime: 3, Patches: midTurn,
			Content: []message.Content{message.ToolResultContent("toolu_1", "bash", "ok", false)}},
	}
}

func encodeAll(t *testing.T, a *Anthropic, msgs []message.Message) ([][]json.RawMessage, form.Snapshot) {
	t.Helper()
	var rows [][]json.RawMessage
	snap := form.Snapshot{}
	for _, m := range msgs {
		r, err := a.encode(m, snap)
		if err != nil {
			t.Fatal(err)
		}
		snap = form.Fold(snap, m.Patches)
		rows = append(rows, r)
	}
	return rows, snap
}

// A set made while a turn runs reaches the bound form before the tool results
// are logged, so the next request in the same turn carries the new effort as a
// system row right after those results (Anthropic's placement in an agentic
// loop), without touching the rows before it.
func TestAnEffortSetMidTurnReachesTheNextRequestInBand(t *testing.T) {
	a := &Anthropic{MaxTokens: 64}
	first, _ := encodeAll(t, a, toolTurn(nil)[:1])
	firstReq, err := a.projectMessagesWithModel(first, form.Snapshot{}.Apply(effortPatch("high")[0]), nil, 64, false, "claude-opus-5-5")
	if err != nil {
		t.Fatal(err)
	}
	rows, snap := encodeAll(t, a, toolTurn(effortPatch("low")))
	req, err := a.projectMessagesWithModel(rows, snap, nil, 64, false, "claude-opus-5-5")
	if err != nil {
		t.Fatal(err)
	}
	if got := roles(t, req); got != "system,user,assistant,user,system" {
		t.Fatalf("roles = %s: the effort row goes after the tool results it follows", got)
	}
	if !strings.Contains(string(req.Messages[0]), `"effort":"high"`) || !strings.Contains(string(req.Messages[4]), `"effort":"low"`) {
		t.Fatalf("effort rows = %s / %s", req.Messages[0], req.Messages[4])
	}
	for i, raw := range firstReq.Messages {
		if unmarked(raw) != unmarked(req.Messages[i]) {
			t.Fatalf("row %d changed between the turn's requests: %s -> %s", i, raw, req.Messages[i])
		}
	}
	if req.OutputConfig != nil {
		t.Fatalf("top-level effort %+v", req.OutputConfig)
	}
}

var cacheMark = regexp.MustCompile(`,?"cache_control":\{[^}]*\}`)

func unmarked(row json.RawMessage) string { return cacheMark.ReplaceAllString(string(row), "") }

func TestATurnWithNoEffortChangeAddsNoRow(t *testing.T) {
	a := &Anthropic{MaxTokens: 64}
	rows, snap := encodeAll(t, a, toolTurn(nil))
	req, err := a.projectMessagesWithModel(rows, snap, nil, 64, false, "claude-opus-5-5")
	if err != nil {
		t.Fatal(err)
	}
	if got := roles(t, req); got != "system,user,assistant,user" {
		t.Fatalf("roles = %s: a tool result with no effort change grew an effort row", got)
	}
	rows, snap = encodeAll(t, a, toolTurn(effortPatch("high")))
	req, err = a.projectMessagesWithModel(rows, snap, nil, 64, false, "claude-opus-5-5")
	if err != nil {
		t.Fatal(err)
	}
	if got := roles(t, req); got != "system,user,assistant,user" {
		t.Fatalf("roles = %s: setting the same effort again grew an effort row", got)
	}
}

func TestAnEffortClearedMidTurnFallsBackToTheDefault(t *testing.T) {
	a := &Anthropic{MaxTokens: 64}
	rows, snap := encodeAll(t, a, toolTurn(effortPatch("")))
	req, err := a.projectMessagesWithModel(rows, snap, nil, 64, false, "claude-opus-5-5")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(req.Messages[4]), `"effort":"`+defaultEffort+`"`) {
		t.Fatalf("roles %s, row %s: a cleared effort must say the default in force", roles(t, req), req.Messages[4])
	}
}

func TestAnOlderModelPicksUpAMidTurnEffortAtTheTopLevel(t *testing.T) {
	a := &Anthropic{MaxTokens: 64}
	rows, snap := encodeAll(t, a, toolTurn(effortPatch("low")))
	req, err := a.projectMessagesWithModel(rows, snap, nil, 64, false, "claude-opus-4-7")
	if err != nil {
		t.Fatal(err)
	}
	if got := roles(t, req); got != "user,assistant,user" {
		t.Fatalf("roles = %s", got)
	}
	if req.OutputConfig == nil || req.OutputConfig.Effort != "low" {
		t.Fatalf("output_config = %+v", req.OutputConfig)
	}
}
