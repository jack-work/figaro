package store

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/internal/arialog"
)

const crashChildEnv = "FIGARO_STORE_CRASH_CHILD"

// A record acknowledged while its fsync stalls is in the segment, not only in
// the daemon's memory: a daemon killed mid-stall keeps it.
func TestADaemonKilledMidStallKeepsTheRecordsItAcknowledged(t *testing.T) {
	if spec := os.Getenv(crashChildEnv); spec != "" {
		crashChild(spec)
		return
	}
	dir := t.TempDir()
	b, err := NewXwalBackend(dir, 0)
	require.NoError(t, err)
	outfit, err := b.CreateOutfit("d", form.Build(form.Snapshot{}, map[string]json.RawMessage{
		"system.provider": json.RawMessage(`"mock"`),
	}, nil))
	require.NoError(t, err)
	conv, err := b.CreateConversation(outfit)
	require.NoError(t, err)
	require.NoError(t, b.Close())

	cmd := exec.Command(os.Args[0], "-test.run=^TestADaemonKilledMidStallKeepsTheRecordsItAcknowledged$", "-test.count=1")
	cmd.Env = append(os.Environ(), crashChildEnv+"="+dir+"|"+conv)
	out, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	require.ErrorAs(t, err, &exit, "the child must die without closing the store: %s", out)
	require.Equal(t, 7, exit.ExitCode(), "the child died early: %s", out)

	b2, err := NewXwalBackend(dir, 0)
	require.NoError(t, err)
	defer b2.Close()
	log, err := b2.OpenFigIR(conv)
	require.NoError(t, err)
	var texts []string
	for _, e := range log.Read() {
		for _, c := range e.Payload.Content {
			texts = append(texts, c.Text)
		}
	}
	require.Contains(t, texts, "acknowledged while the disk stalled")
}

func crashChild(spec string) {
	var dir, conv string
	for i := range spec {
		if spec[i] == '|' {
			dir, conv = spec[:i], spec[i+1:]
		}
	}
	b, err := NewXwalBackend(dir, 0)
	if err != nil {
		fmt.Println("open:", err)
		os.Exit(2)
	}
	b.store.syncs.before = func() { select {} }
	log, err := b.OpenFigIR(conv)
	if err != nil {
		fmt.Println("open log:", err)
		os.Exit(2)
	}
	if _, err := log.Append(arialog.Entry[message.Message]{Payload: message.Message{
		Role:    message.RoleInput,
		Content: []message.Content{message.TextContent("acknowledged while the disk stalled")},
	}}); err != nil {
		fmt.Println("append:", err)
		os.Exit(2)
	}
	os.Exit(7)
}
