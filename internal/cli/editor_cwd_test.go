package cli

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jack-work/figaro/api/form"
	"github.com/stretchr/testify/require"
)

func editorRecorder(t *testing.T) (spec string, argvFile string) {
	t.Helper()
	dir := t.TempDir()
	argvFile = filepath.Join(dir, "argv")
	spec = filepath.Join(dir, "fake-editor")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + argvFile + "\n"
	require.NoError(t, os.WriteFile(spec, []byte(script), 0o755))
	return spec, argvFile
}

func editorInput(t *testing.T, cwd, id string) (*interactiveInput, *searchInputTerminal, chan struct{}) {
	t.Helper()
	reader := &stubHistoryReader{history: transcriptHistory(12)}
	tc := newRecordingTerminal()
	in := newSearchInteractiveInput(reader, tc.searchInputTerminal)
	in.tc = tc
	in.suspendTerm, in.resumeTerm = func() {}, func() {}
	in.figaroID = id
	in.bound = &formMirror{}
	if cwd != "" {
		in.bound.snap = form.Snapshot{}.Apply(form.Build(form.Snapshot{},
			map[string]json.RawMessage{"system.cwd": json.RawMessage(`"` + cwd + `"`)}, nil))
	}
	done := make(chan struct{})
	go func() { in.run(); close(done) }()
	return in, tc.searchInputTerminal, done
}

// openByKeys reaches openPath the way the pager does: Ctrl+Enter in visual
// mode, on the input goroutine, which is what lets the editor take the
// terminal. The text opened is the test's, not the word under the cursor.
func openByKeys(in *interactiveInput, tc *searchInputTerminal, text string) {
	in.mu.Lock()
	in.lt.tr.openPath = func(string) { in.openPath(text) }
	in.lt.tr.key('v')
	in.lt.tr.key('w')
	in.mu.Unlock()
	tc.send([]byte("\x1b[13;5u"))
}

func plainEditorRunner(t *testing.T) {
	t.Helper()
	prev := runEditorCommand
	runEditorCommand = func(argv []string) error {
		cmd := exec.Command(argv[0], argv[1:]...)
		return cmd.Run()
	}
	t.Cleanup(func() { runEditorCommand = prev })
}

func waitArgv(t *testing.T, file string) []string {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(file); err == nil && len(b) > 0 {
			return strings.Split(strings.TrimSpace(string(b)), "\n")
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("the editor never ran")
	return nil
}

// The pager's own cwd is elsewhere: a relative path must land in the figaro's.
func TestTheEditorResolvesARelativePathInTheFigarosCwd(t *testing.T) {
	t.Setenv("FIGARO_STATE_DIR", t.TempDir())
	spec, argvFile := editorRecorder(t)
	t.Setenv("VISUAL", spec)
	plainEditorRunner(t)

	work := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(work, "src"), 0o755))
	target := filepath.Join(work, "src", "main.go")
	require.NoError(t, os.WriteFile(target, []byte("x\n"), 0o644))
	t.Chdir(t.TempDir())

	in, tc, done := editorInput(t, work, "")
	openByKeys(in, tc, "src/main.go:7")
	require.Equal(t, []string{target}, waitArgv(t, argvFile))
	tc.send([]byte{0x04})
	waitSignal(t, done, "input loop exit")
}

func TestTheEditorTakesAnAbsolutePathAsItIs(t *testing.T) {
	t.Setenv("FIGARO_STATE_DIR", t.TempDir())
	spec, argvFile := editorRecorder(t)
	t.Setenv("VISUAL", spec)
	plainEditorRunner(t)

	elsewhere := filepath.Join(t.TempDir(), "notes.md")
	require.NoError(t, os.WriteFile(elsewhere, []byte("x\n"), 0o644))

	in, tc, done := editorInput(t, t.TempDir(), "")
	openByKeys(in, tc, elsewhere)
	require.Equal(t, []string{elsewhere}, waitArgv(t, argvFile))
	tc.send([]byte{0x04})
	waitSignal(t, done, "input loop exit")
}

func TestTheEditorRefusesAPathOnAnotherNode(t *testing.T) {
	t.Setenv("FIGARO_STATE_DIR", t.TempDir())
	spec, argvFile := editorRecorder(t)
	t.Setenv("VISUAL", spec)
	plainEditorRunner(t)

	work := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(work, "main.go"), []byte("x\n"), 0o644))
	t.Chdir(work)

	in, tc, done := editorInput(t, work, "cccb44ac1234/9f5bc9fa5678")
	openByKeys(in, tc, "main.go")
	require.Eventually(t, func() bool {
		in.mu.Lock()
		defer in.mu.Unlock()
		for _, n := range in.lt.tr.notes.newestFirst() {
			if strings.Contains(n.text, "main.go: the path is on another node (cccb44ac1234)") {
				return true
			}
		}
		return false
	}, 3*time.Second, 10*time.Millisecond)
	_, err := os.Stat(argvFile)
	require.True(t, os.IsNotExist(err), "the editor ran for a remote figaro")
	tc.send([]byte{0x04})
	waitSignal(t, done, "input loop exit")
}
