package anthropic

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
)

// THE FINGERPRINT IS THE MECHANISM; THIS IS ITS TRIPWIRE.
//
// Every cached translation entry carries a fingerprint, and lookupCached
// REFUSES any entry whose fingerprint differs from the current one. So a
// change to how a message encodes is handled by bumping the version: stale
// bytes become UNREACHABLE rather than merely detected. That is the guarantee,
// and it is structural.
//
// IT HAS A HOLE, AND UNTIL THIS TEST THE HOLE WAS UNGUARDED. The dangerous
// accident is not bumping the version by mistake -- it is CHANGING THE
// ENCODING AND NOT BUMPING, which silently mixes old and new renderings under
// one fingerprint, with the per-LT cache making whichever ran first permanent.
//
// Nothing pinned it. The closest assertion, commit_test.go:33, is
//
//	assert.Equal(t, a.Fingerprint(), native.Fingerprint)
//
// which compares the function TO ITSELF: it passes unchanged if the encoding
// moves and the version does not. A grep for the literal "anthropic/.../v5"
// across internal/ finds nothing. Found by aria 3a9225b1, verified before
// being acted on.
//
// THE SHAPE, AND WHY IT IS ONE MECHANISM RATHER THAN TWO DISCIPLINES: the
// golden file is KEYED BY THE FINGERPRINT.
//
//	change the encoding          -> the golden no longer matches, so you
//	                                must regenerate
//	regenerate without bumping   -> you would overwrite the EXISTING key,
//	                                which this test refuses
//	bump the version             -> a new key, no collision, golden written
//
// So "the fingerprint moved when the encoding did" stops being a habit
// somebody has to remember and becomes a fact the suite enforces.
//
// REGENERATING: bump Fingerprint(), then run with FIGARO_GOLDEN=1.
func TestEncodingChangeRequiresAFingerprintBump(t *testing.T) {
	a := &Anthropic{Model: "claude-test", MaxTokens: 64}
	fp := a.Fingerprint()

	// A fixture that exercises the shapes the encoder actually branches on:
	// a prose input, an assistant reply, a tool invoke and its result.
	msgs := []message.Message{
		{Role: message.RoleInput, LogicalTime: 1,
			Content: []message.Content{message.TextContent("what is the time")}},
		{Role: message.RoleOutput, LogicalTime: 2, StopReason: message.StopToolInvoke,
			Content: []message.Content{
				message.TextContent("checking"),
				{Type: message.ContentToolInvoke, ToolCallID: "call_1", ToolName: "bash",
					Arguments: map[string]any{"command": "date"}},
			}},
		{Role: message.RoleInput, LogicalTime: 3,
			Content: []message.Content{
				{Type: message.ContentToolResult, ToolCallID: "call_1", Text: "Tue Aug 18"},
			}},
		{Role: message.RoleOutput, LogicalTime: 4, StopReason: message.StopEnd,
			Content: []message.Content{message.TextContent("it is Tuesday")}},
	}

	var encoded [][]json.RawMessage
	for _, m := range msgs {
		blocks, err := a.encode(m, form.Snapshot{})
		if err != nil {
			t.Fatalf("encode LT %d: %v", m.LogicalTime, err)
		}
		encoded = append(encoded, blocks)
	}
	got, err := json.MarshalIndent(encoded, "", "  ")
	if err != nil {
		t.Fatal(err)
	}

	// The fingerprint contains '/', which is a path separator.
	name := strings.ReplaceAll(fp, "/", "_") + ".json"
	path := filepath.Join("testdata", name)

	if os.Getenv("FIGARO_GOLDEN") != "" {
		if _, err := os.Stat(path); err == nil {
			// THE COLLISION IS THE POINT. Regenerating under an existing key
			// means the encoding moved while the fingerprint stood still.
			t.Fatalf("REFUSING TO OVERWRITE %s.\n"+
				"The encoding changed but Fingerprint() still returns %q. Cached entries written\n"+
				"under that fingerprint would be served for bytes this code no longer produces,\n"+
				"and the per-LT cache makes whichever ran first permanent.\n"+
				"BUMP THE VERSION IN Fingerprint(), then re-run with FIGARO_GOLDEN=1.", path, fp)
		}
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("wrote golden for fingerprint %q -> %s", fp, path)
		return
	}

	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("no golden for fingerprint %q (%s).\n"+
			"If you just bumped the version this is expected: re-run with FIGARO_GOLDEN=1.\n"+
			"If you did NOT bump it, the fingerprint is not what this build produces and the\n"+
			"cache's staleness guarantee does not hold: %v", fp, path, err)
	}
	if string(got) != string(want) {
		t.Fatalf("THE ENCODING CHANGED UNDER AN UNCHANGED FINGERPRINT %q.\n"+
			"lookupCached refuses entries whose fingerprint differs, so entries cached under\n"+
			"this fingerprint will be served in place of the bytes this code now produces --\n"+
			"old and new renderings mixed under one key, permanently.\n"+
			"BUMP THE VERSION IN Fingerprint(), then re-run with FIGARO_GOLDEN=1.\n\n"+
			"want (%d bytes):\n%s\n\ngot (%d bytes):\n%s",
			fp, len(want), truncGolden(string(want)), len(got), truncGolden(string(got)))
	}
}

func truncGolden(s string) string {
	if len(s) > 1200 {
		return s[:1200] + "\n... (truncated)"
	}
	return s
}
