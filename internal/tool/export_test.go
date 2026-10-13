package tool

import (
	"context"
	"testing"

	"github.com/jack-work/figaro/api/sandbox"
	"github.com/jack-work/figaro/internal/sandbox/local"
)

func staticCwd(dir string) func() string { return func() string { return dir } }

func localBox(t *testing.T, dir string) sandbox.Sandbox {
	t.Helper()
	box, err := local.Provider{Host: local.NewHost(local.DefaultTTL), Scope: t.Name()}.Open(context.Background(), sandbox.Spec{Workspace: dir})
	if err != nil {
		t.Fatal(err)
	}
	return box
}
