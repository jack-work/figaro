package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// The rule is EXISTENCE, so the fixture is a real directory with real files in
// it: a stub that answers yes to a shape would be testing the shape.
func TestEditorTargetIsExistenceThenCoordinate(t *testing.T) {
	dir := t.TempDir()
	home := filepath.Join(dir, "home")
	require.NoError(t, os.MkdirAll(filepath.Join(home, "notes"), 0o755))
	write := func(rel string) string {
		p := filepath.Join(dir, rel)
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte("x\n"), 0o644))
		return p
	}
	plain := write("src/main.go")
	colon := write("odd/main.go:12") // a colon is legal in a filename
	write("home/notes/todo.md")
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "adir"), 0o755))

	for _, tc := range []struct {
		name string
		text string
		path string
		line int
		ok   bool
	}{
		{name: "absolute", text: plain, path: plain, ok: true},
		{name: "relative to cwd", text: "src/main.go", path: plain, ok: true},
		{name: "line", text: "src/main.go:41", path: plain, line: 41, ok: true},
		{name: "line and column", text: "src/main.go:41:7", path: plain, line: 41, ok: true},
		{name: "tilde", text: "~/notes/todo.md", path: filepath.Join(home, "notes/todo.md"), ok: true},
		// The file that is really named "main.go:12" wins over reading the
		// suffix as a coordinate: what is on disk outranks the pattern.
		{name: "colon in the name", text: "odd/main.go:12", path: colon, ok: true},
		{name: "not a file", text: "src/missing.go", ok: false},
		{name: "a directory is not a file", text: "adir", ok: false},
		{name: "not a path at all", text: "attending", ok: false},
		{name: "empty", text: "   ", ok: false},
		{name: "bare number suffix", text: "src/main.go:0", ok: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path, line, ok := editorTarget(tc.text, dir, home, isRegularFile)
			require.Equal(t, tc.ok, ok)
			if !tc.ok {
				return
			}
			require.Equal(t, tc.path, path)
			require.Equal(t, tc.line, line)
		})
	}
}

func TestEditorArgvCarriesLineOnlyToVim(t *testing.T) {
	for _, tc := range []struct {
		name string
		spec string
		line int
		want []string
	}{
		{name: "nvim", spec: "nvim", line: 41, want: []string{"nvim", "+41", "/tmp/f"}},
		{name: "vim by path", spec: "/usr/bin/vim", line: 7, want: []string{"/usr/bin/vim", "+7", "/tmp/f"}},
		{name: "vim with flags", spec: "vim -p", line: 7, want: []string{"vim", "-p", "+7", "/tmp/f"}},
		{name: "no line", spec: "nvim", line: 0, want: []string{"nvim", "/tmp/f"}},
		{name: "other editor drops the line", spec: "code -w", line: 41, want: []string{"code", "-w", "/tmp/f"}},
		{name: "quoted argument survives", spec: "sh -c 'echo hi'", want: []string{"sh", "-c", "echo hi", "/tmp/f"}},
		{name: "unset", spec: "", line: 3, want: nil},
		{name: "blank", spec: "   ", line: 3, want: nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, editorArgv(tc.spec, "/tmp/f", tc.line))
		})
	}
}

// The gate is the whole of "the editor does not fight the reader for
// keystrokes": prove it parks, and prove it lets go.
func TestInputGateParksAndResumes(t *testing.T) {
	var g inputGate
	g.park() // nothing requested: never blocks

	paused, resume := g.request()
	parked := make(chan struct{})
	go func() {
		g.park()
		close(parked)
	}()
	<-paused
	select {
	case <-parked:
		t.Fatal("the reader left the gate before it was released")
	default:
	}
	close(resume)
	<-parked
}
