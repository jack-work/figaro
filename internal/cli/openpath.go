package cli

// THE PAGER HANDS THE TERMINAL OVER, AND TAKES IT BACK.
//
// `a` over text that is not an aria id asks this file whether the text names a
// file, and if it does, runs the configured editor on the terminal the pager
// is holding. Three things have to be arranged for that, and none of them is
// optional:
//
//   - the input loop must stop reading stdin, or figaro and the editor race
//     for every keystroke;
//   - the terminal must go back to what the shell handed us (cooked, normal
//     screen, no mouse reporting) and then be taken again;
//   - the editor must be the FOREGROUND process group, or the tty's Ctrl-C
//     lands on figaro as well, and figaro's default action for SIGINT is to
//     die.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// editorTarget decides whether text names a file and, if so, which line of it.
// The rule is existence: after a leading ~ is expanded and a relative path is
// resolved against cwd, os.Stat has to find a file. A `:LINE` or `:LINE:COL`
// suffix is stripped only when the text with it kept is NOT a file, because a
// colon is a legal character in a filename and what is on disk outranks what
// the pattern suggests.
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

// coordCandidates is the text itself, then the text with a trailing :LINE or
// :LINE:COL taken off, in that order.
func coordCandidates(text string) []pathCandidate {
	out := []pathCandidate{{text: text}}
	rest, n, ok := trimCoord(text)
	if !ok {
		return out
	}
	if rest2, n2, ok2 := trimCoord(rest); ok2 {
		// :LINE:COL -- the first number off the end is the column.
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

// editorArgv builds the command line. The line number travels only to editors
// that understand `+N`; anything else is given the path and nothing else,
// because a stray argument is worse than a lost coordinate.
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

// openPath is the transcript's path hook. IT RUNS ON THE INPUT GOROUTINE WITH
// THE RENDER LOCK HELD, which decides the shape of everything below: the
// decision is made here (it only stats), the pause is REQUESTED here (so it is
// ordered before the read loop's next Read, with no handshake to race), and
// the work happens on a goroutine of its own.
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

// editorParkWait bounds how long the editor waits for the read loop to park.
// It parks as soon as the current input chunk is drained, so exceeding this
// means there is no read loop to park: the terminal is not ours to give away.
const editorParkWait = 2 * time.Second

func (in *interactiveInput) runEditor(argv []string, path string, paused <-chan struct{}, resume chan<- struct{}) {
	select {
	case <-paused:
	case <-time.After(editorParkWait):
		close(resume) // whoever parks later leaves immediately
		in.note("editor: the terminal is busy")
		return
	}
	defer close(resume)

	if in.suspendTerm == nil || in.resumeTerm == nil {
		in.note("editor: this session does not own a terminal")
		return
	}
	// Hold the frame. Every renderer entry point records that the screen is
	// stale and returns while a batch is open, so nothing paints over the
	// editor; the resume below voids the painter's model of the screen and
	// draws one full frame.
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

// runEditorCommand runs the editor on the real terminal, as its own foreground
// process group. A non-zero exit is reported, not raised: the reader is coming
// back to the pager either way.
func runEditorCommand(argv []string) error {
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	cmd.SysProcAttr = editorProcAttr()
	if err := cmd.Start(); err != nil {
		return err
	}
	err := cmd.Wait()
	// The child took the terminal's foreground group with it; without this,
	// the next read from stdin raises SIGTTIN and stops figaro.
	reclaimTerminal()
	return err
}
