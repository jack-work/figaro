package cli

// THE COMMAND LANGUAGE IS THE CLI'S, and this file is the proof rather than the
// promise: a ':' line is tokenized into argv and handed to THE SAME
// cmdkit.Router that `figaro` dispatches at a shell. There is no second table,
// no second parser, and no second set of flags. `:ls -a`, `:doctor mem`,
// `:status --id x` work the day they work at a shell, because they are the same
// call.
//
// Three things had to be true for that, and two of them were already:
//
//  1. The router's writers are FIELDS (Router.Stdout/Stderr), so output can be
//     captured instead of printed. Already true.
//  2. Process exit is a VARIABLE (exitProcess), so a die() deep inside a verb
//     can be turned into something recoverable. Already true -- the test suite
//     swaps it for a panic, and this reuses that seam. It is still the dodgiest
//     thing here; see plans/transcript-command-mode.md.
//  3. The verbs that TAKE OVER THE TERMINAL cannot run inside a pager that
//     already owns it. Those are overlaid (below) rather than run.

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/jack-work/figaro/internal/config"
)

// tokenize splits a command line into argv the way a shell would: whitespace
// separates, quotes group, backslash escapes inside double quotes and outside.
// It is deliberately small -- no expansion, no globbing, no substitution -
// because a command line in a pager is a place to name things, not a shell.
func tokenize(line string) []string {
	var (
		out  []string
		cur  strings.Builder
		have bool // a token has begun (so "" is a real empty token)
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

// ---------------------------------------------------------------------------
// The overlay: verbs that mean something different with a transcript open.
// ---------------------------------------------------------------------------

// overlayVerbs are the ones the pager answers itself. Two reasons a verb lands
// here, and only two:
//
//   - IT WOULD TAKE THE TERMINAL. `listen`, `send`, `new`, `replay` and `fork`
//     open a renderer of their own, and there is already one on the screen.
//   - GLUCK'S RULE: "since the transcript is ambiently open, whatever the
//     result is should replace the current transcript". A verb that RESOLVES an
//     aria shows it. So `attend` binds AND displays, where at a shell it only
//     binds.
//
// Everything else -- ls, status, doctor, state, set, unset, queue, kill,
// promote, gc, models, show... -- falls through to the real router untouched.
var overlayVerbs = map[string]bool{
	"open": true, "o": true, "listen": true,
	"attend": true, "at": true,
	"send": true, "s": true,
	"new": true, "replay": true, "fork": true,
	"import": true, // reads os.Stdin for `-` (portable.go)
	"login":  true, // an interactive OAuth flow with its own prompts
}

// terminalSubverbs are verb+subverb pairs that take the terminal, which a bare
// verb name cannot express. `figaro form` is a perfectly good in-process
// command; `figaro form listen` is a FOLLOWER -- it reads os.Stdin in a loop
// and only returns on q/^C.
//
// Found by asking the question Gluck asked ("does form listen work in the
// status bar?"), and the answer is worse than no: run in command mode it would
// block the command goroutine forever, holding routerMu so every later command
// hangs -- AND read os.Stdin concurrently with the pager's own input loop, two
// readers racing for one fd, each swallowing half the user's keystrokes.
var terminalSubverbs = map[string]bool{
	"vault unlock": true, // prompts for a passphrase
}

// READING A FORM IS ONE THING, and in the pager it is the live view.
//
// `form listen` was hosted and `form show` was not, so the same question --
// "what is on this aria's board?" -- reached the pit down two different roads.
// `show` went through the router, was captured as TEXT, and became an output
// pit whose rows are lines: a motion moved among the lines that happened to
// carry an aria-shaped id, which is why the selection appeared to skip whole
// properties. There is no defensible difference between the two verbs inside a
// pager -- a pager repaints; a snapshot in a repainting window is just a live
// view that lies when the form changes -- so both are the live view now, and
// `form`/`state` with no subverb is too.
//
// formSubverbs are the ones that DO something else with a form and still go to
// the router. Everything not in this set is a read.
var formSubverbs = map[string]bool{
	"set": true, "delete": true, "unset": true,
	"new": true, "fork": true, "ls": true, "rm": true,
	"outfit": true, "help": true,
}

// liveForm decides whether argv is a form READ, and names the aria it reads.
// The spec is the first positional that is not the subverb, or --id's value;
// empty means "the aria on screen", per THE SUBJECT rule.
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
			// A FLAG THE PIT CANNOT HONOUR is a command, not a view: `-j`
			// wants JSON on a stdout the pit does not have.
			return "", "", false
		default:
			return name, a, true
		}
	}
	return name, "", true
}

// runCommand is the ':' box's hook, and it is CALLED WITH THE RENDER LOCK HELD
// -- the input loop takes that lock around every keystroke, and the box's Enter
// is a keystroke like any other.
//
// So this function does exactly one thing: hand off. It may not take the lock
// (Go mutexes do not recurse) and it may not block (the input goroutine is the
// only thing reading the keyboard). MEASURED, in a pty, before this hand-off
// existed: `:open <id>` froze the whole pager, dead, with the box still on
// screen -- the input goroutine parked on a mutex it was already holding.
func (in *interactiveInput) runCommand(line string) { go in.execCommand(line) }

func (in *interactiveInput) execCommand(line string) {
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

// runOverlay answers the verbs the pager owns.
func (in *interactiveInput) runOverlay(verb string, args []string) {
	switch verb {
	case "open", "o", "listen":
		in.commandAsync(func(ctx context.Context) (string, error) {
			return in.switchSubject(ctx, strings.Join(args, " "), false)
		})
	case "attend", "at":
		in.commandAsync(func(ctx context.Context) (string, error) {
			return in.switchSubject(ctx, strings.Join(args, " "), true)
		})
	case "send", "s":
		in.commandAsync(func(ctx context.Context) (string, error) {
			return in.commandSend(ctx, args)
		})
	default:
		in.note(fmt.Sprintf("%s: not available inside the pager (it opens a view of its own)", verb))
	}
}

// ---------------------------------------------------------------------------
// The router, run in-process.
// ---------------------------------------------------------------------------

// routerMu serializes in-process router runs. THE DODGE: exitProcess is a
// package-level variable, so swapping it is not safe against a concurrent
// second command -- and commands run on their own goroutines. One at a time is
// the cheap correct answer while the real fix (verbs that return errors instead
// of calling die) is unwritten.
var routerMu sync.Mutex

// runThroughRouter dispatches argv to the CLI's own table and shows whatever it
// wrote. Output goes to a panel, because `:ls` is thirty lines and the status
// row is one.
// THE SUBJECT IS THE IMPLIED ARIA. At a shell, a verb with no target falls back
// to the binding -- `figaro state` means "the aria this shell attends". In the
// pager that is the wrong default by a mile: there is an aria ON SCREEN, and it
// is what the reader means. Measured before this rule: `:state` inside a live
// transcript answered "no figaro bound to this shell", which is true and
// useless.
//
// So a command that ACCEPTS --id and was given no target gets the subject. The
// rule is deliberately narrow -- it never overrides a target the user typed,
// and it never invents one for a verb that does not take an aria at all.
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
			return argv // the user named a target; never second-guess it
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

func (in *interactiveInput) runThroughRouter(argv []string) {
	argv = in.withSubject(argv)
	in.note("…" + strings.Join(argv, " "))
	go func() {
		out, code := in.routeCaptured(argv)
		lines := splitOutputLines(out)
		switch {
		case len(lines) == 0 && code == 0:
			in.note(argv[0] + ": ok")
		case len(lines) == 0:
			in.note(fmt.Sprintf("%s: failed (%d)", argv[0], code))
		case len(lines) == 1:
			in.note(lines[0])
		default:
			in.mu.Lock()
			in.lt.setTranscriptCmdOut(strings.Join(argv, " "), lines)
			in.mu.Unlock()
		}
	}()
}

// routeCaptured runs one command with the router's writers pointed at a buffer
// and process exit turned into a panic we catch.
func (in *interactiveInput) routeCaptured(argv []string) (string, int) {
	routerMu.Lock()
	defer routerMu.Unlock()

	var buf lockedBuffer
	r := buildRouter("figaro", in.loaded)
	r.Stdout = &buf
	r.Stderr = &buf

	code := 0
	func() {
		// An abort unwinds to here and becomes this command's exit code. No
		// global is swapped for it any more: die() always panics, and the
		// boundary decides what that means (see exitPanic).
		prevOut, prevErr := stdout, stderrw
		// AND the package's ordinary-output writer, which is where the verbs
		// actually write: the router's Stdout field only catches what the ROUTER
		// prints (help, usage, its own diagnostics). Measured before this line:
		// `:ls` painted its table straight over the pager's status row.
		stdout, stderrw = &buf, &buf
		defer func() {
			stdout, stderrw = prevOut, prevErr
			if rec := recover(); rec != nil {
				if ep, ok := rec.(exitPanic); ok {
					code = int(ep)
					return
				}
				// A real panic in a verb must not take the pager with it
				// either: report it as the failure it is.
				fmt.Fprintf(&buf, "panic: %v\n", rec)
				code = 1
			}
		}()
		code = r.Run(argv)
	}()
	return buf.String(), code
}

// lockedBuffer is a writer a command can hand to a goroutine of its own without
// racing the read that follows. Verbs do that (the queue fetch, the tree walk),
// and a data race in a dry run is still a data race.
type lockedBuffer struct {
	mu sync.Mutex
	b  strings.Builder
}

func (w *lockedBuffer) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.b.Write(p)
}

func (w *lockedBuffer) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.b.String()
}

func splitOutputLines(s string) []string {
	s = strings.TrimRight(s, "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

// ---------------------------------------------------------------------------
// Completion: the router's own, again rather than a second copy.
// ---------------------------------------------------------------------------

// complete returns the candidates for a partially typed command line. It calls
// the SAME hidden `__complete` verb the shell completion scripts call, so aria
// ids, form ids, flags and prompt-context refs are all completed here by the
// code that already knew how.
func (in *interactiveInput) complete(line string) []string {
	argv := tokenize(line)
	// A trailing space means "the cursor is on a fresh word".
	atBoundary := line == "" || strings.HasSuffix(line, " ")
	current := ""
	if !atBoundary && len(argv) > 0 {
		current = argv[len(argv)-1]
		argv = argv[:len(argv)-1]
	}
	if len(argv) == 0 {
		// Completing the VERB itself: the router's own names, plus the overlay
		// aliases that exist only here.
		return matchPrefix(append(commandVerbs(in.loaded), "open", "at"), current)
	}
	req := append([]string{"__complete", argv[0], "--current", current, "--"}, argv[1:]...)
	out, _ := in.routeCaptured(req)
	return matchPrefix(splitOutputLines(out), current)
}

// commandVerbs is every verb the router knows.
func commandVerbs(loaded *config.Loaded) []string {
	return buildRouter("figaro", loaded).CommandNames()
}

func matchPrefix(cands []string, prefix string) []string {
	out := cands[:0:0]
	for _, c := range cands {
		if c == "" {
			continue
		}
		// The completion protocol allows "value\tdescription"; take the value.
		if i := strings.IndexByte(c, '\t'); i >= 0 {
			c = c[:i]
		}
		if strings.HasPrefix(c, prefix) {
			out = append(out, c)
		}
	}
	return out
}

// commonPrefix is what Tab inserts when several candidates share a head: the
// longest thing that cannot be wrong.
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

// runLive hosts a live verb in the pit. The view is built off the input
// goroutine (it dials) and installed under the render lock.
func (in *interactiveInput) runLive(name, spec string) { in.openLive(name, spec, false) }

// openLive dials the verb off the input goroutine and installs it under the
// render lock. full opens the pit at the pane's height, which is what
// `fig form listen` asks for.
func (in *interactiveInput) openLive(name, spec string, full bool) {
	if spec == "" {
		spec = in.currentID() // the aria on screen, per THE SUBJECT rule
	}
	// A PIT THAT OPENS FULLSCREEN IS ITS OWN ANNOUNCEMENT. `:form show` says
	// what it is doing because the reader typed it into a box and the answer
	// takes a moment to dial; `fig form listen` opens straight into the thing,
	// and a "…form show <id>" alert over it is the program narrating itself.
	if !full {
		in.note("…" + name + " " + spec)
	}
	go func() {
		view, closeView, err := openFormView(spec, in.loaded, in.renderLocked)
		if err != nil {
			in.note(name + ": " + err.Error())
			return
		}
		view.closeConn = closeView
		// THE PIT host repaints the pager; the view never writes anywhere.
		view.repaint = in.renderLocked
		in.mu.Lock()
		in.lt.tr.showLivePit(name, view, full)
		in.mu.Unlock()
	}()
}

// renderLocked repaints the pager from a background pump.
func (in *interactiveInput) renderLocked() {
	in.mu.Lock()
	in.lt.render()
	in.mu.Unlock()
}
