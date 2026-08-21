package store

// Validation fixture generator (final-validation aria, 2026-07-25).
//
// TestGeneratePerformanceFixture writes a CONSTANT body, so a duplicated
// message at a page boundary is invisible in it. Acceptance row P2 needs the
// opposite: every message uniquely identifiable on screen, so a human (or a
// script) reading a rendered frame can see a repeat. This writes one aria whose
// every message announces its own ordinal.
//
//	FIGARO_VAL_FIXTURE=<state-root> FIGARO_VAL_TURNS=200 FIGARO_VAL_LINES=6 \
//	  go test ./internal/store -run TestGenerateNumberedFixture -v

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/jack-work/figaro/api/message"
)

func TestGenerateNumberedFixture(t *testing.T) {
	root := os.Getenv("FIGARO_VAL_FIXTURE")
	if root == "" {
		t.Skip("set FIGARO_VAL_FIXTURE to generate an isolated store")
	}
	turns := performanceFixtureInt(t, "FIGARO_VAL_TURNS", 200)
	lines := performanceFixtureInt(t, "FIGARO_VAL_LINES", 6)

	backend, err := NewXwalBackend(root, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()
	outfit, err := backend.CreateOutfit("numbered", message.Patch{
		Set: map[string]json.RawMessage{
			"system.provider": json.RawMessage(`"copilot"`),
			"system.model":    json.RawMessage(`"gpt-5.6-sol"`),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	id, err := backend.CreateConversation(outfit)
	if err != nil {
		t.Fatal(err)
	}
	log, err := backend.OpenFigIR(id)
	if err != nil {
		t.Fatal(err)
	}
	// Each message is a block of lines that all carry the same ordinal, so a
	// duplicate shows up as the same ordinal twice, at any scroll position.
	body := func(tag string, n int) string {
		var b strings.Builder
		for i := 0; i < lines; i++ {
			fmt.Fprintf(&b, "%s-%04d line %d of %d: unique marker %s%04d.%d\n",
				tag, n, i+1, lines, tag, n, i+1)
		}
		return b.String()
	}
	for n := 1; n <= turns; n++ {
		for _, m := range []struct {
			role message.Role
			tag  string
		}{{message.RoleInput, "ASK"}, {message.RoleOutput, "REPLY"}} {
			if _, err := log.Append(Entry[message.Message]{Payload: message.Message{
				Role:    m.role,
				Content: []message.Content{message.TextContent(body(m.tag, n))},
			}}); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := backend.SetMeta(id, &AriaMeta{
		MessageCount: turns * 2,
		LastFigaroLT: uint64(turns*2 + 2),
	}); err != nil {
		t.Fatal(err)
	}
	fmt.Fprintf(os.Stderr, "NUMBERED_FIXTURE_ARIA=%s turns=%d\n", id, turns)
}
