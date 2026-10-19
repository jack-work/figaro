package cli

// A ':' line is tokenized into argv and dispatched through the same
// cmdkit.Router the shell uses. Verbs that take over the terminal are overlaid
// instead.

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/jack-work/figaro/api/form"
	"github.com/jack-work/figaro/internal/cmdkit"
	"strings"
	"sync"
	"time"

	"github.com/jack-work/figaro/internal/config"
)

// tokenize splits like a shell: whitespace separates, quotes group, backslash
// escapes. No expansion.
func tokenize(line string) []string {
	var (
		out  []string
		cur  strings.Builder
		have bool
	)
	flush := func() {
		if have {
			out = append(out, cur.String())
			cur.Reset()
			have = false
		}
	}
	rs := []rune(line)
	for i := 0; i < len(rs); i++ {
		c := rs[i]
		switch c {
		case ' ', '\t':
			flush()
		case '\\':
			if i+1 < len(rs) {
				i++
				cur.WriteRune(rs[i])
				have = true
			}
		case '\'':
			have = true
			for i++; i < len(rs) && rs[i] != '\''; i++ {
				cur.WriteRune(rs[i])
			}
		case '"':
			have = true
			for i++; i < len(rs) && rs[i] != '"'; i++ {
				if rs[i] == '\\' && i+1 < len(rs) {
					i++
				}
				cur.WriteRune(rs[i])
			}
		default:
			cur.WriteRune(c)
			have = true
		}
	}
	flush()
	return out
}

// overlayVerbs are answered by the pager itself: they would take the terminal,
// or they resolve an aria and so must show it.
var overlayVerbs = map[string]bool{
	"notifications": true, "no": true,
	"listen": true,
	"attend": true, "at": true,
	"send": true, "s": true,
	"decide": true,
	"new":    true, "replay": true, "fork": true,
	"kill":   true, // may kill the aria on screen, and must then move
	"import": true, // reads os.Stdin for `-`
	// models is a pit; as a routed verb it would print to os.Stdout.
	"models": true,
}

// terminalSubverbs take the terminal: `form listen` reads os.Stdin in a loop,
// which would race the pager's input loop for the fd and hold routerMu forever.
var terminalSubverbs = map[string]bool{
	"vault unlock":    true,
	"vault init":      true,
	"vault reset":     true,
	"provider login":  true,
	"provider logout": true,
	"troupe login":    true,
	"troupe join":     true,
	"troupe leave":    true,
}

// A form read in the pager is the live view; formSubverbs do something else and
// go to the router.
var formSubverbs = map[string]bool{
	"set": true, "delete": true, "unset": true,
	"new": true, "fork": true, "ls": true, "rm": true,
	"outfit": true, "help": true,
}

// liveForm decides whether argv is a form read; an empty spec means the aria on screen.
func liveForm(argv []string) (name, spec string, ok bool) {
	if len(argv) == 0 || (argv[0] != "form" && argv[0] != "state") {
		return "", "", false
	}
	rest := argv[1:]
	name = "form show"
	if len(rest) > 0 {
		if formSubverbs[rest[0]] {
			return "", "", false
		}
		if rest[0] == "show" || rest[0] == "listen" {
			name, rest = "form "+rest[0], rest[1:]
		}
	}
	for i := 0; i < len(rest); i++ {
		a := rest[i]
		switch {
		case a == "--id" || a == "-i":
			if i+1 < len(rest) {
				return name, rest[i+1], true
			}
		case strings.HasPrefix(a, "--id="):
			return name, strings.TrimPrefix(a, "--id="), true
		case strings.HasPrefix(a, "-"):
			// A flag the pit cannot honour (-j wants a stdout) is a
			// command, not a view.
			return "", "", false
		default:
			return name, a, true
		}
	}
	return name, "", true
}

// runCommand is called with the render lock held: it may not take the lock or
// block, so it hands off.
func (in *interactiveInput) runCommand(line string) { go in.execCommand(line) }

func (in *interactiveInput) execCommand(line string) {
	if isCommentVerb(line) {
		in.commandComment(line)
		return
	}
	argv := tokenize(line)
	if len(argv) == 0 {
		return
	}
	verb := argv[0]
	if overlayVerbs[verb] {
		in.runOverlay(verb, argv[1:])
		return
	}
	if name, spec, ok := liveForm(argv); ok {
		in.runLive(name, spec)
		return
	}
	if len(argv) > 1 && terminalSubverbs[verb+" "+argv[1]] {
		in.note(verb + " " + argv[1] + ": not available inside the pager (it reads the keyboard)")
		return
	}
	in.runThroughRouter(argv)
}

func (in *interactiveInput) runOverlay(verb string, args []string) {
	switch verb {
	case "listen":
		in.commandAsync(func(ctx context.Context) (string, error) {
			spec, err := oneSpec(verb, args)
			if err != nil {
				return "", err
			}
			return in.switchSubject(ctx, spec, false)
		})
	case "attend", "at":
		in.commandAsync(func(ctx context.Context) (string, error) {
			spec, err := oneSpec("attend", args)
			if err != nil {
				return "", err
			}
			return in.switchSubject(ctx, spec, true)
		})
	case "kill":
		in.askKill(args)
	case "send", "s":
		in.commandAsync(func(ctx context.Context) (string, error) {
			return in.commandSend(ctx, args)
		})
	case "fork":
		in.commandAsync(func(ctx context.Context) (string, error) {
			return in.commandFork(ctx, args)
		})
	case "decide":
		in.commandDecide(args)
	case "notifications", "no":
		in.mu.Lock()
		if len(args) > 0 && args[0] == "clear" {
			in.lt.tr.notes.clear()
			in.lt.tr.setCommandNote("notifications cleared")
		} else {
			in.lt.tr.closePanels()
			in.lt.tr.openNotificationsPit()
			in.lt.tr.render()
		}
		in.mu.Unlock()
	case "models":
		in.openModels()
	default:
		in.note(fmt.Sprintf("%s: not available inside the pager (it opens a view of its own)", verb))
	}
}

// openModels fetches off the input goroutine (it dials) and installs under the
// render lock.
func (in *interactiveInput) openModels() {
	in.progress("…models")
	go func() {
		models, warnings := fetchModels(in.loaded)
		current := in.subjectModel()
		view := newModelsView(models, current, in.chooseModel)
		in.mu.Lock()
		in.lt.tr.showLivePit("models", view, false)
		in.mu.Unlock()
		if len(models) == 0 && len(warnings) > 0 {
			in.note("models: " + strings.Join(warnings, "; "))
		}
	}()
}

func (in *interactiveInput) subjectModel() string {
	cli := in.aria()
	if cli == nil {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()
	resp, err := cli.Form(ctx)
	if err != nil {
		return ""
	}
	raw, ok := resp.Snapshot.Get("system.model")
	if !ok {
		return ""
	}
	return valueSummary(raw)
}

// chooseModel writes system.model and system.provider in one patch: a model is
// only meaningful to its own provider.
func (in *interactiveInput) chooseModel(c modelChoice) {
	cli := in.aria()
	id := in.currentID()
	if cli == nil {
		in.noteErr("models: no aria on screen to set")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()
	set := map[string]json.RawMessage{"system.model": mustJSON(c.model)}
	if c.provider != "" {
		set["system.provider"] = mustJSON(c.provider)
	}
	if _, err := cli.Set(ctx, form.Build(form.Snapshot{}, set, nil), 0); err != nil {
		in.noteErr("models: " + wireErrorText(err))
		return
	}
	in.note(fmt.Sprintf("model: %s (%s) on %s", c.model, c.provider, ariaName(id)))
}

func mustJSON(s string) json.RawMessage {
	b, _ := json.Marshal(s)
	return b
}

// routerMu serializes in-process router runs: the router's writers are
// package-level, and commands run on their own goroutines.
var routerMu sync.Mutex

// withSubject gives a command that accepts --id and was given no target the
// aria on screen; it never overrides a typed target.
func (in *interactiveInput) withSubject(argv []string) []string {
	if len(argv) == 0 {
		return argv
	}
	r := buildRouter("figaro", in.loaded)
	cmd, ok := r.Command(argv[0])
	if !ok || cmd.PassRaw {
		return argv
	}
	takesID := false
	for _, f := range cmd.Flags {
		if f.Long == "id" {
			takesID = true
			break
		}
	}
	if !takesID {
		return argv
	}
	for _, a := range argv[1:] {
		if a == "--id" || a == "-i" || strings.HasPrefix(a, "--id=") {
			return argv
		}
		if a == "--" {
			break
		}
	}
	id := in.currentID()
	if id == "" {
		return argv
	}
	return append(append([]string{}, argv...), "--id", id)
}

// pagerUncapped drops `list`'s row cap inside the pager; an explicit -a, -n or
// -j is left alone.
func pagerUncapped(argv []string) []string {
	if len(argv) == 0 {
		return argv
	}
	switch argv[0] {
	case "ls", "list", "lsh":
	default:
		return argv
	}
	for _, a := range argv[1:] {
		switch {
		case a == "--", a == "-a", a == "--all", a == "-j", a == "--json":
			return argv
		case a == "-n", a == "--limit", strings.HasPrefix(a, "-n"), strings.HasPrefix(a, "--limit="):
			return argv
		}
	}
	return append(append([]string{}, argv...), "--all")
}

func (in *interactiveInput) runThroughRouter(argv []string) {
	typed := strings.Join(argv, " ")
	argv = in.withSubject(pagerUncapped(argv))
	go func() {
		finished := in.progressIfSlow("…" + typed)
		out, code := in.routeCaptured(argv)
		finished()
		lines := splitOutputLines(out.text)
		switch {
		case len(lines) == 0 && code == 0:
			in.note(argv[0] + ": ok")
		case len(lines) == 0:
			in.note(fmt.Sprintf("%s: failed (%d)", argv[0], code))
		case len(lines) == 1:
			in.note(lines[0])
		default:
			in.mu.Lock()
			in.lt.setTranscriptCmdOut(strings.Join(argv, " "), out.rows(lines))
			in.mu.Unlock()
		}
	}()
}

// routeCaptured runs one command with the writers pointed at a buffer and die()
// turned into a recovered panic.
func (in *interactiveInput) routeCaptured(argv []string) (captured, int) {
	routerMu.Lock()
	defer routerMu.Unlock()

	var buf lockedBuffer
	r := buildRouter("figaro", in.loaded)
	r.Stdout = &buf
	r.Stderr = &buf

	code := 0
	func() {
		prevOut, prevErr := stdout, stderrw
		// The package's ordinary-output writer is swapped too: the router's
		// Stdout field only catches what the router itself prints.
		stdout, stderrw = &buf, &buf
		defer func() {
			stdout, stderrw = prevOut, prevErr
			if rec := recover(); rec != nil {
				if ep, ok := rec.(exitPanic); ok {
					code = int(ep)
					return
				}
				fmt.Fprintf(&buf, "panic: %v\n", rec)
				code = 1
			}
		}()
		code = r.Run(argv)
	}()
	return buf.captured(), code
}

// captured is what a command printed, plus the id each line is about, declared
// by the verb at write time (emitRow): never parsed back out of rendered text.
type captured struct {
	text string
	ids  map[int]string
}

// A line with no id is chrome: not selectable.
func (c captured) rows(lines []string) []pitRow {
	out := make([]pitRow, 0, len(lines))
	for i, line := range lines {
		id := c.ids[i]
		out = append(out, pitRow{text: line, yank: id, id: id})
	}
	return out
}

// lockedBuffer is safe for a verb that writes from a goroutine of its own.
type lockedBuffer struct {
	mu    sync.Mutex
	b     strings.Builder
	lines int
	ids   map[int]string
}

func (w *lockedBuffer) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.lines += strings.Count(string(p), "\n")
	return w.b.Write(p)
}

func (w *lockedBuffer) WriteRow(line, id string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if id != "" {
		if w.ids == nil {
			w.ids = map[int]string{}
		}
		w.ids[w.lines] = id
	}
	w.b.WriteString(line)
	w.b.WriteByte('\n')
	w.lines++
}

func (w *lockedBuffer) captured() captured {
	w.mu.Lock()
	defer w.mu.Unlock()
	return captured{text: w.b.String(), ids: w.ids}
}

func splitOutputLines(s string) []string {
	s = strings.TrimRight(s, "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

// complete calls the same hidden `__complete` verb the shell scripts call,
// adding the aria on screen and a path fallback.
func (in *interactiveInput) complete(line string) []string {
	argv := tokenize(line)
	atBoundary := line == "" || strings.HasSuffix(line, " ")
	current := ""
	if !atBoundary && len(argv) > 0 {
		current = argv[len(argv)-1]
		argv = argv[:len(argv)-1]
	}
	if len(argv) == 0 {
		verbs := append(buildRouter("figaro", in.loaded).CommandCandidates(),
			cmdkit.Candidate("at", "jump to a turn or node: at <turn>[.<node>]"),
			cmdkit.Candidate("notifications", "what figaro said, and what went wrong (space n)"))
		return matchPrefix(verbs, current)
	}
	req := append([]string{"__complete", argv[0], "--describe", "--current", current, "--"}, argv[1:]...)
	// in.figaroID, not in.currentID(): the completer runs from dispatch, which
	// holds in.mu.
	completionSubject = in.figaroID
	out, _ := in.routeCaptured(req)
	completionSubject = ""
	cands := matchPrefix(splitOutputLines(out.text), current)
	if len(cands) == 0 && looksLikePath(current) {
		cands = pathCandidatesFor(&cmdkit.CompleteContext{Describe: true}, current, false)
	}
	return cands
}

func commandVerbs(loaded *config.Loaded) []string {
	return buildRouter("figaro", loaded).CommandNames()
}

func matchPrefix(cands []string, prefix string) []string {
	out := cands[:0:0]
	for _, c := range cands {
		if c == "" {
			continue
		}
		if v, _ := cmdkit.SplitCandidate(c); strings.HasPrefix(v, prefix) {
			out = append(out, c)
		}
	}
	return out
}

func commonPrefix(ss []string) string {
	if len(ss) == 0 {
		return ""
	}
	p := ss[0]
	for _, s := range ss[1:] {
		for !strings.HasPrefix(s, p) {
			p = p[:len(p)-1]
			if p == "" {
				return ""
			}
		}
	}
	return p
}

// oneSpec takes exactly one positional; joining words would make `:attend a b`
// an aria called "a b".
func oneSpec(verb string, args []string) (string, error) {
	switch len(args) {
	case 0:
		return "", nil
	case 1:
		return args[0], nil
	}
	return "", fmt.Errorf("%s takes one aria: %s <id|@role>[:<turn>]", verb, verb)
}

func (in *interactiveInput) runLive(name, spec string) { in.openLive(name, spec, false) }

// openLive dials off the input goroutine and installs under the render lock.
func (in *interactiveInput) openLive(name, spec string, full bool) {
	if spec == "" {
		spec = in.currentID()
	}
	if !full {
		in.progress("…" + name + " " + spec)
	}
	go func() {
		view, closeView, err := openFormView(spec, in.loaded, in.renderLocked)
		if err != nil {
			in.note(name + ": " + err.Error())
			return
		}
		view.closeConn = closeView
		view.repaint = in.renderLocked
		in.mu.Lock()
		in.lt.tr.showLivePit(name, view, full)
		in.mu.Unlock()
	}()
}

func (in *interactiveInput) renderLocked() {
	in.mu.Lock()
	in.lt.render()
	in.mu.Unlock()
}

const progressDelay = 150 * time.Millisecond

func (in *interactiveInput) progressIfSlow(msg string) (finished func()) {
	var mu sync.Mutex
	done := false
	timer := time.AfterFunc(progressDelay, func() {
		mu.Lock()
		defer mu.Unlock()
		if !done {
			in.progress(msg)
		}
	})
	return func() {
		mu.Lock()
		done = true
		mu.Unlock()
		timer.Stop()
	}
}
