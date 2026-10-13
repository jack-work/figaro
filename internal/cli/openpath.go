package cli

// Handing the terminal to an editor: the input loop must stop reading stdin,
// the terminal must go back to cooked/normal screen/no mouse and be retaken,
// and the editor must be the foreground process group or the tty's Ctrl-C lands
// on figaro too.
import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// editorTarget: a `:LINE` or `:LINE:COL` suffix is stripped only when the text
// with it kept is not a file, since a colon is legal in a filename.
func editorTarget(text, cwd, home string, isFile func(string) bool) (path string, line int, ok bool) {
	text = strings.TrimSpace(text)
	if text == "" {
		return "", 0, false
	}
	for _, c := range coordCandidates(text) {
		p := resolveEditorPath(c.text, cwd, home)
		if p != "" && isFile(p) {
			return p, c.line, true
		}
	}
	return "", 0, false
}

type pathCandidate struct {
	text string
	line int
}

func coordCandidates(text string) []pathCandidate {
	out := []pathCandidate{{text: text}}
	rest, n, ok := trimCoord(text)
	if !ok {
		return out
	}
	if rest2, n2, ok2 := trimCoord(rest); ok2 {
		out = append(out, pathCandidate{text: rest2, line: n2})
	}
	out = append(out, pathCandidate{text: rest, line: n})
	return out
}

func trimCoord(text string) (rest string, n int, ok bool) {
	i := strings.LastIndexByte(text, ':')
	if i <= 0 || i == len(text)-1 {
		return text, 0, false
	}
	v, err := strconv.Atoi(text[i+1:])
	if err != nil || v <= 0 {
		return text, 0, false
	}
	return text[:i], v, true
}

func resolveEditorPath(text, cwd, home string) string {
	switch {
	case text == "~":
		return home
	case strings.HasPrefix(text, "~/"):
		if home == "" {
			return ""
		}
		return filepath.Join(home, text[2:])
	case filepath.IsAbs(text):
		return filepath.Clean(text)
	case cwd == "":
		return filepath.Clean(text)
	default:
		return filepath.Join(cwd, text)
	}
}

func isRegularFile(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.Mode().IsRegular()
}

// The line number travels only to editors that understand `+N`.
func editorArgv(spec, path string, line int) []string {
	argv := tokenize(spec)
	if len(argv) == 0 {
		return nil
	}
	if line > 0 && takesPlusLine(argv[0]) {
		argv = append(argv, "+"+strconv.Itoa(line))
	}
	return append(argv, path)
}

func takesPlusLine(cmd string) bool {
	switch filepath.Base(cmd) {
	case "vi", "vim", "nvim", "view", "gvim":
		return true
	}
	return false
}

// openPath runs on the input goroutine with the render lock held: it decides
// here (a stat), requests the pause here so it orders before the next Read, and
// works on its own goroutine.
func (in *interactiveInput) openPath(text string) {
	cwd, _ := os.Getwd()
	path, line, ok := editorTarget(text, cwd, os.Getenv("HOME"), isRegularFile)
	if !ok {
		go in.note(text + " is not an aria id or a file")
		return
	}
	argv := editorArgv(in.loaded.Editor(), path, line)
	if len(argv) == 0 {
		go in.note("no editor configured: set editor under [cli] in config.toml")
		return
	}
	paused, resume := in.gate.request()
	go in.runEditor(argv, path, paused, resume)
}

// Exceeding editorParkWait means there is no read loop to park: the terminal is
// not ours to give away.
const editorParkWait = 2 * time.Second

func (in *interactiveInput) runEditor(argv []string, path string, paused <-chan struct{}, resume chan<- struct{}) {
	select {
	case <-paused:
	case <-time.After(editorParkWait):
		close(resume)
		in.note("editor: the terminal is busy")
		return
	}
	defer close(resume)

	if in.suspendTerm == nil || in.resumeTerm == nil {
		in.note("editor: this session does not own a terminal")
		return
	}
	// Hold the frame: every renderer entry point returns while a batch is open,
	// so nothing paints over the editor.
	in.mu.Lock()
	in.lt.tr.beginBatch()
	in.mu.Unlock()

	in.suspendTerm()
	err := runEditorCommand(argv)
	in.resumeTerm()

	in.mu.Lock()
	in.lt.tr.screenMoved()
	in.lt.tr.endBatch()
	in.lt.tr.renderNow()
	in.mu.Unlock()

	if err != nil {
		in.note("editor: " + err.Error())
		return
	}
	in.note("edited " + filepath.Base(path))
}

// The editor runs as its own foreground process group; a non-zero exit is
// reported, not raised.
func runEditorCommand(argv []string) error {
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	cmd.SysProcAttr = editorProcAttr()
	if err := cmd.Start(); err != nil {
		return err
	}
	err := cmd.Wait()
	// Without reclaiming the foreground group, the next read from stdin raises
	// SIGTTIN and stops figaro.
	reclaimTerminal()
	return err
}
