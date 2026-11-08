package cli

// Handing the terminal to an editor: the input loop must stop reading stdin,
// the terminal must go back to cooked/normal screen/no mouse and be retaken,
// and the editor must be the foreground process group or the tty's Ctrl-C lands
// on figaro too.
import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/jack-work/figaro/sdk/aria"
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
	if node := foreignNode(in.figaroID); node != "" {
		go in.note(text + ": the path is on another node (" + node + ")")
		return
	}
	if cwd, ok := in.subjectCwd(); ok {
		in.openPathIn(text, cwd)
		return
	}
	if in.editorSpec() == "" {
		go in.note("no editor: set editor under [cli] in config.toml, or $VISUAL / $EDITOR")
		return
	}
	paused, resume := in.gate.request()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), editorFetchWait)
		cwd, err := in.daemonDir(ctx)
		cancel()
		var argv []string
		path := ""
		if err == nil {
			p, line, ok := editorTarget(text, cwd, os.Getenv("HOME"), isRegularFile)
			if ok {
				path, argv = p, editorArgv(in.editorSpec(), p, line)
			} else {
				err = errors.New(text + " is not an aria id or a file")
			}
		}
		if err != nil {
			select {
			case <-paused:
			case <-time.After(editorParkWait):
			}
			close(resume)
			in.note(err.Error())
			return
		}
		in.runEditor(argv, "edited "+filepath.Base(path), paused, resume)
	}()
}

func (in *interactiveInput) openPathIn(text, cwd string) {
	path, line, ok := editorTarget(text, cwd, os.Getenv("HOME"), isRegularFile)
	if !ok {
		go in.note(text + " is not an aria id or a file")
		return
	}
	argv := editorArgv(in.editorSpec(), path, line)
	if len(argv) == 0 {
		go in.note("no editor: set editor under [cli] in config.toml, or $VISUAL / $EDITOR")
		return
	}
	paused, resume := in.gate.request()
	go in.runEditor(argv, "edited "+filepath.Base(path), paused, resume)
}

func (in *interactiveInput) subjectCwd() (string, bool) {
	if in.bound == nil {
		return "", false
	}
	snap, _, _ := in.bound.state()
	if c := snap.Lookup(cwdKey); c != nil && *c != "" {
		return *c, true
	}
	return "", false
}

func (in *interactiveInput) daemonDir(ctx context.Context) (string, error) {
	acli, err := in.angelus()
	if err != nil {
		return "", err
	}
	dir, err := daemonCwd(ctx, acli.Status)
	if err != nil {
		return "", fmt.Errorf("editor: the figaro has no %s, and %w", cwdKey, err)
	}
	return dir, nil
}

func (in *interactiveInput) editorSpec() string {
	if in.loaded != nil {
		if spec := in.loaded.Editor(); spec != "" {
			return spec
		}
	}
	return cmp.Or(strings.TrimSpace(os.Getenv("VISUAL")), strings.TrimSpace(os.Getenv("EDITOR")))
}

func inputOpenEditor(in *interactiveInput, _ keyEvent) keyVerdict {
	if text, ok := in.editorSource(); ok {
		in.openText(text)
	}
	return keyHandled
}

func (in *interactiveInput) editorSource() (func(context.Context) (string, error), bool) {
	in.mu.Lock()
	defer in.mu.Unlock()
	tr := in.lt.tr
	if tr.visual.highlighted() {
		text := tr.visualText()
		return func(context.Context) (string, error) { return text, nil }, true
	}
	plan, selected := in.lt.transcriptSelectionPlan()
	if !selected {
		tr.setCommandNoteAt("nothing selected: ^N/^P selects messages, v V highlights", alertError)
		tr.render()
		return nil, false
	}
	return func(ctx context.Context) (string, error) {
		return selectionText(plan, transcriptPageSize, func(at aria.Anchor, limit int) (aria.Page, error) {
			return in.fcli.ReadBefore(ctx, at, wireBudget(limit))
		})
	}, true
}

func (in *interactiveInput) openText(text func(context.Context) (string, error)) {
	if in.editorSpec() == "" {
		go in.note("no editor: set editor under [cli] in config.toml, or $VISUAL / $EDITOR")
		return
	}
	paused, resume := in.gate.request()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), editorFetchWait)
		body, err := text(ctx)
		cancel()
		var path string
		if err == nil {
			path, err = writeTempText(body)
		}
		if err != nil {
			select {
			case <-paused:
			case <-time.After(editorParkWait):
			}
			close(resume)
			in.note("editor: " + err.Error())
			return
		}
		defer os.Remove(path)
		in.runEditor(editorArgv(in.editorSpec(), path, 0), "", paused, resume)
	}()
}

const editorFetchWait = 10 * time.Second

func writeTempText(text string) (string, error) {
	f, err := os.CreateTemp("", "figaro-selection-*.md")
	if err != nil {
		return "", err
	}
	_, err = f.WriteString(text)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
}

// Exceeding editorParkWait means there is no read loop to park: the terminal is
// not ours to give away.
const editorParkWait = 2 * time.Second

func (in *interactiveInput) runEditor(argv []string, done string, paused <-chan struct{}, resume chan<- struct{}) {
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
	if done != "" {
		in.note(done)
	}
}

var runEditorCommand = runEditorForeground

// The editor runs as its own foreground process group; a non-zero exit is
// reported, not raised.
func runEditorForeground(argv []string) error {
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
