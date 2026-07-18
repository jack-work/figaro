package store

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"testing"

	"github.com/jack-work/figaro/internal/message"
)

func TestGeneratePerformanceFixture(t *testing.T) {
	root := os.Getenv("FIGARO_PERF_FIXTURE")
	if root == "" {
		t.Skip("set FIGARO_PERF_FIXTURE to generate an isolated store")
	}
	arias := performanceFixtureInt(t, "FIGARO_PERF_ARIAS", 100)
	messages := performanceFixtureInt(t, "FIGARO_PERF_MESSAGES", 2)

	backend, err := NewXwalBackend(root)
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()
	loadout, err := backend.CreateLoadout("performance", message.Patch{
		Set: map[string]json.RawMessage{
			"system.provider": json.RawMessage(`"copilot"`),
			"system.model":    json.RawMessage(`"gpt-5.6-sol"`),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	body := "synthetic performance history"
	for i := 0; i < arias; i++ {
		id, err := backend.CreateConversation(loadout)
		if err != nil {
			t.Fatal(err)
		}
		log, err := backend.Open(id)
		if err != nil {
			t.Fatal(err)
		}
		for j := 0; j < messages; j++ {
			role := message.RoleUser
			if j%2 == 1 {
				role = message.RoleAssistant
			}
			if _, err := log.Append(Entry[message.Message]{Payload: message.Message{
				Role:    role,
				Content: []message.Content{message.TextContent(body)},
			}}); err != nil {
				t.Fatal(err)
			}
		}
		if err := backend.SetMeta(id, &AriaMeta{
			MessageCount: messages,
			LastActiveMS: int64(i + 1),
			LastFigaroLT: uint64(messages + 2),
		}); err != nil {
			t.Fatal(err)
		}
	}
}

func performanceFixtureInt(t *testing.T, key string, fallback int) int {
	t.Helper()
	raw := os.Getenv(key)
	if raw == "" {
		return fallback
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 0 {
		t.Fatalf("%s=%q is not a non-negative integer", key, raw)
	}
	fmt.Fprintf(os.Stderr, "%s=%d\n", key, value)
	return value
}
