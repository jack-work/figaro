package tool_test

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jack-work/figaro/api/message"
	"github.com/jack-work/figaro/api/sandbox"
	"github.com/jack-work/figaro/internal/sandbox/local"
	"github.com/jack-work/figaro/internal/tool"
)

func openScoped(t *testing.T, host *local.Host, scope, dir string) sandbox.Sandbox {
	t.Helper()
	if dir == "" {
		dir = t.TempDir()
	}
	box, err := local.Provider{Host: host, Scope: scope}.Open(context.Background(), sandbox.Spec{Workspace: dir})
	require.NoError(t, err)
	return box
}

func openBox(t *testing.T, dir string) sandbox.Sandbox {
	return openScoped(t, local.NewHost(local.DefaultTTL), t.Name(), dir)
}

func newBash(t *testing.T, dir string) *tool.BashTool {
	return &tool.BashTool{Box: openBox(t, dir), CwdFn: func() string { return dir }}
}

func newRead(t *testing.T, dir string) *tool.ReadTool {
	return &tool.ReadTool{Box: openBox(t, dir), CwdFn: func() string { return dir }}
}

func newEdit(t *testing.T, dir string) *tool.EditTool {
	return &tool.EditTool{Box: openBox(t, dir), CwdFn: func() string { return dir }}
}

func resultText(content []message.Content) string {
	var sb strings.Builder
	for _, c := range content {
		if c.Type == message.ContentProse {
			sb.WriteString(c.Text)
		}
	}
	return sb.String()
}
