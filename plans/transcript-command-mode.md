# Command mode

> **STATUS: PLAN, plus a DRY RUN you can test.** Written 2026-08-23. The dry
> run is committed on `feat/command-mode` and is throwaway by agreement —
> its job was to find the pitfalls in §5, and it found four.
>
> Insert mode is **punted entirely**, at Gluck's instruction: *"Only once
> command mode is perfected should we try to implement an insert mode."*
> [transcript-composer.md](transcript-composer.md) is parked until then.
> [transcript-subject.md](transcript-subject.md) holds the lineage/prefix design
> that this work eventually consumes.

## What it is

`:` opens a command line in the transcript. Its grammar is the CLI's — one
grammar, two front doors — and a bare coordinate is still a goto, exactly as
`:12` is in vim while `:w` writes.

| typed | means |
|---|---|
| `:12`, `:12.3`, `:0` | go to that coordinate (unchanged) |
| `:open <spec>` | look at another aria. **Attendance is untouched** — this is `figaro listen` |
| `:listen <spec>` | the same verb under the shell's name for it |
| `:attend <spec>`, `:at <spec>` | bind this shell to it **and** look at it |
| `:send [<spec>] -- <text>` | send; with no spec, to the aria on screen |

And `:` opens the pager when pressed in incipit, which it never used to.

### The rule that decides the semantics

Gluck's, and it is the whole reason `:attend` differs from the shell's:

> *"I want congruent semantics to the cli, but since the transcript is ambiently
> open, whatever the result is should replace the current transcript."*

So a verb that RESOLVES an aria shows it. `figaro attend <id>` at a shell binds
and prints; `:attend <id>` binds and **shows**, because attending an aria you
cannot see is not a thing a reader of this pager ever means. `:listen` alone
does not touch attendance, which is precisely what `listen` means at a shell.

## 1. What the dry run proves

Driven in a real pty against a real daemon, no fixtures in the loop:

- **The subject switches in-process.** `:open <beta>` replaced the content and
  the footer of a pager opened on alpha. No re-exec, no flash.
- **`:at` really attends.** After `:at <alpha>`, leaving the pager and running
  `figaro status` **in that same pane** resolved to alpha with no argument. The
  binding is real, not cosmetic.
- **`:send` reaches the drain.** `:send -- also say STEEROK` typed into a
  running turn came back classified as a **steer**, rendered `↳ input · Gluck`,
  and the model answered "TWO STEEROK". The verb needs no steer affordance for
  the same reason `figaro send` does not: timing decides, not the UI.
- **`:` opens the pager from incipit**, mid-stream, with the box up.
- **The error paths report** into the status row: unknown verb, unknown aria, a
  prompt with no `--`.

## 2. What the dry run does NOT do

Deliberately, and each is scoped below:

- **A send session refuses to change aria** (§5.4). It says so.
- **The switch is a true reload.** Nothing is retained across it — not even
  between a parent and its own fork. That is
  [transcript-subject.md §3](transcript-subject.md), and it is the next
  conversation.
- **No `:fork`, `:queue`, `:set`, `:kill`, `:list`.** The four verbs above were
  enough to find the pitfalls; adding more before the constructor is fixed would
  just be more code to move.
- **The verbs are hand-written, not the CLI's own.** §4 is about why that must
  change and what it costs.

## 3. The architecture the dry run arrived at

Three pieces, and the shape of each was forced rather than chosen.

**`livelogTurn.retarget(id, status)` and `transcript.retarget(client, …)`.**
What survives a subject change is *the reader's posture* — the pane, the panels
they had open, the verbose toggle — and what does not is everything about the
aria: the window, the row cache, the selection, every derived index, and any
walk or search that was aimed at the old coordinates. `wireClient` was extracted
from `newLivelogTurn` so the fold is installed in one place rather than copied
into the switch path.

**The initial connection comes in through the switch path.** `figaro listen`
now calls the same `in.retarget` that `:open` calls. *A door used once a session
is a door that rots*; making startup use it means the switch is exercised on
every run.

**The generation fence, which is the one piece I would keep verbatim.** Every
notify handler and desync hook captures the subject generation it was born with
and checks it before touching the renderer. The old connection's pump is still
live while the new one dials, and its frames carry the **old aria's
coordinates** — folding one into the new store renders one conversation under
another's turn ids, which is the fabricated-adjacency bug class the range store
exists to prevent, at aria scale.

## 4. The real work: the command language IS the CLI

The dry run hand-writes four verbs. That is the one thing about it I would not
keep, because it makes a second dialect — and a second dialect drifts.

> **The transcript's command line must run the CLI's own parser.** One grammar,
> two front doors.

What blocks that today: a CLI verb is fused to its process wrapper. It gets its
connection from `WithAngelus(...)`, reports failure with `die()` (which calls
`os.Exit`, and inside the pager would take the terminal with it), and writes
results to `os.Stdout` — which, while the alt screen is up, paints **over the
frame**, the bleed the smoke suite already documents.

So the work is: **separate every verb's body from its wrapper.** Roughly

```go
// what a verb is, once it is not also a program
type verb func(ctx context.Context, env *verbEnv, args []string) (string, error)
```

where `env` carries the clients and the verb returns a line instead of printing
one. The shell front door keeps `die()` and stdout; the pager front door renders
the returned line into the status row. This is a refactor with no user-visible
change and a wide blast radius, and it is the thing to be careful about — but it
is also the only way `:send` and `figaro send` are *provably* the same command
rather than two things that look alike.

**Do it verb by verb, behind the four that already work.** Each converted verb
deletes a hand-written twin.

## 5. The four pitfalls, which are the deliverable

Each was found in a terminal. **None of them is visible to a unit test**, and
three of them are properties of the program's *shape* rather than of any
function.

### 5.1 A hook armed on one of two doors is armed on neither

The command runner was wired inside `seedSubject`'s already-open branch. Cold
starts take the other branch — and every start is a cold start. So the box
answered *"commands need a live session"* on the one path every session takes.
The pattern generalises: `setQueuedFetch`, `setHistoryFetcher`, `setCatchUp` and
now `setCommandRunner` are four hooks armed by hand at two call sites. **That is
a constructor waiting to be written.**

### 5.2 Deadlock: the hook runs under the render lock

The input loop takes the render lock around every keystroke, and the `:` box's
Enter is a keystroke. The first version of `runCommand` took that lock again to
write its status note, and **parked the input goroutine on a mutex it was
already holding** — the whole pager froze, dead, with the box still on screen
and no way out but SIGKILL.

`runCommand` is now exactly one `go` statement, and the field it is stored in
says why. The rule for the real work:

> **Anything reachable from a key action may not take the render lock and may
> not block.** It hands off, or it is a bug that presents as a hung terminal.

### 5.3 The pager has two front doors and they do not share a constructor

`listen.go` and `stream.go` each build an `interactiveInput` by hand. Command
mode wired at one of them was **dead in the other** — and the other is `figaro
send`, which is the surface most people are looking at. Fixing it meant editing
two struct literals and remembering four hooks.

### 5.4 …and the connection has two owners

This is 5.3's sharp end and the most instructive failure of the day.

In a send session the aria connection belongs to `mustPromptFigaro`, which is
blocked on its `Done()`. The switch closed that connection to replace it — and
**the session ended mid-turn**: the pager vanished, the shell prompt came back,
and the agent kept working with nobody watching.

The silent half is worse. That path's notify pump is **not** fenced by the
subject generation (it was created before the input loop existed), so a switch
there would have folded the old aria's frames into the new aria's store — the
exact corruption §3's fence exists to prevent, arriving through the one door the
fence does not cover.

Both are one cause: **whoever dials must be whoever switches.** The dry run
refuses to switch in a send and says so; the real fix is one constructor that
owns the connection, and an outer loop that waits on `subjectDead` rather than
on a connection it captured.

## 6. Phases

| # | phase | done when |
|---|---|---|
| 1 | **one constructor** for `interactiveInput`, owning the connection and arming every hook; both doors use it; the send path's wait moves to `subjectDead` | `:open` works in a send, and 5.3/5.4 cannot recur |
| 2 | **verbs return lines**: the `verbEnv`/`verb` split, `:send` and `:open` converted first | `:send` and `figaro send` share a parser, proven by a test that runs both |
| 3 | **the rest of the verbs**: `:fork`, `:queue`, `:set`, `:list`, `:kill` | each deletes a hand-written twin |
| 4 | **retention across a switch** — [transcript-subject.md §3](transcript-subject.md): lineage on the wire, keep the common prefix | a fork-follow does not re-read the shared prefix |
| 5 | **role subjects**: `:open @role` follows the bearer when it is recast | recast a role; the transcript moves by itself |

Note `:open @role` **already resolves** today, because `resolveFigaroTargetEndpoint`
follows `target-aria` for the CLI and the dry run reuses it. What phase 5 adds is
*following* — noticing the recast and switching again.

## 7. Questions

**1. Does `:open` push a stack — is there `:back`?** Browsing a fork tree wants
one, and it is cheap now (the old connection could stay warm). It also implies
`:tree`, which is `figaro ls` rendered in the pager.

**2. What should `:q` do?** In the pager `q` already means "leave the pager". Is
`:q` the same, or does it end the process (vim's meaning), or is it unbound? I
lean unbound: two ways to leave is one too many, and `:q` ending a `listen` while
`q` merely closes the pager is the kind of near-miss that costs a session.

**3. When `:open` lands on an aria mid-turn, do we follow the tail?** The dry run
does — it opens at the live edge like `listen`. The alternative is to land where
you last were in that aria, which needs a per-aria memory of position and is a
different feature.

**4. Should the command line have history?** Up/Down through previously typed
commands. It is the first thing I reach for in any `:` box, and it is a small
amount of state — but it is also the first piece of *the editor* work, which
Gluck may want to keep for when the shared text box lands.

**5. Does `:send <other-aria> -- text` show anything?** Today it reports "sent to
<id>" and stays where it is. Should sending elsewhere also switch to it — i.e.
is `:send <id>` an aria-resolving verb by Gluck's rule in §0, or is "resolving"
only about the verbs whose *purpose* is to name an aria? I read it as the latter
and would keep `:send` where it is, but it is exactly the kind of thing the rule
does not settle by itself.

**6. Error rendering.** A verb's failure currently gets one clipped line in the
status row. Some CLI errors are three lines with a suggestion (`unknown command
"shwo" / did you mean…`). Does command mode get a transient panel for those, or
does it clip and let the reader run it at a shell?

---

# Round 2: the router itself, and the dodginess

> Added 2026-08-23 after Gluck's instruction: *"refactor the cli parser to be a
> generic parser that will work on the command input as well… I also need
> bash/emacs style keybindings in the command mode, history, and autocomplete…
> try to get me something that works, and then call out all the dodginess you
> needed introduce."*

## What now works

`:` dispatches to **the same `cmdkit.Router` the shell dispatches to**. No second
table, no second parser, no second flag set. `:ls`, `:doctor mem`, `:status`,
`:state`, `:set mantra "café bar"` all work because they are the same call.

- **Output** longer than one line lands in a footer panel beside `?` and `!`;
  one line or less goes to the status row.
- **The subject is the implied aria** (§R2.2).
- **A line editor** with a cursor, emacs motions, history and Tab completion
  (§R2.3).
- **Overlaid verbs**: `open`/`listen`/`attend`/`at`/`send` keep pager semantics;
  `new`/`replay`/`fork` refuse, because they open a renderer and one is already
  on the screen.

## R2.1 The refactor this forced: 154 print sites

The router's writers were already fields (`Router.Stdout`), so I expected capture
to be free. **It was not.** The verbs do not write through the router — they
write to `os.Stdout` directly, 154 times. The first `:ls` painted its table
straight over the pager's status row, live, in the terminal.

So `internal/cli` grew two package-level writers:

```go
var stdout  io.Writer = os.Stdout   // a command's RESULT
var stderrw io.Writer = os.Stderr   // a command's DIAGNOSTIC (die, warnings)
```

and every `fmt.Print*`, every `fmt.Fprint*(os.Stdout|os.Stderr, …)` and every
`json.NewEncoder(os.Stdout)` **in a command body** now goes through them. The
renderer files are excluded by name — incipit and the pager write escape
sequences, and their output *is* the terminal rather than a result.

`die()` moved onto `stderrw` too, so a verb's failure text is captured instead of
being painted under the alt screen, where it went nowhere.

This is the refactor worth keeping whatever happens to the rest.

## R2.2 The subject is the implied aria

At a shell, a verb with no target falls back to the binding. In the pager that is
the wrong default by a mile: **there is an aria on screen and it is what the
reader means.** Measured: `:state` in a live transcript answered *"no figaro
bound to this shell"* — true, and useless.

So a command that accepts `--id` and was given no target gets the subject
appended. Deliberately narrow: it never overrides a target the user typed, and
never invents one for a verb that takes no aria.

**Not covered, and it is a gap**: verbs that take a POSITIONAL aria and no
`--id` flag. `:show` still means the shell's binding. Fixing that needs the
router to say "this positional is an aria", which is a `Command` field that does
not exist.

## R2.3 The line editor

The box was `q += string(b)` gated on `b < 0x7f`. It is now `lineEditor`:
runes with a UTF-8 accumulator, a cursor drawn in reverse video (the pager hides
the real one), emacs motions `^A ^E ^B ^F ^K ^U ^W`, history on `^P`/`^N` and
Up/Down, and Tab completion that calls **the router's own hidden `__complete`
verb** — so aria ids, form ids and flags are completed by the code that already
knew how.

Typing `café` into it now yields `café`.

**`^D` is deliberately not delete-forward.** `0x04` is detach at the input level
in every mode, the two action arrays must agree about a chord, and between "the
key that gets you out of a terminal program" and "delete one character forward",
the escape hatch wins.

**`^N`/`^P` are history here**, not node selection — the one remap, and the
reason is that a box with history and a box with a node cursor are different
boxes.

## R2.4 THE DODGINESS, all of it

Ordered by how much I dislike it.

**1. Swapped package globals, serialized by a mutex.** `exitProcess`, `stdout`
and `stderrw` are swapped for the duration of one in-process command and restored
after. `routerMu` makes it one command at a time. This is a **process-global
mutation to achieve a per-call effect**, and it is wrong in three ways: a
concurrent verb that spawns its own goroutine and prints after returning writes
into a buffer that has been handed away; a background verb outliving the swap
prints into the void; and nothing stops a future caller from running two commands
at once. *The real fix is `RunContext` carrying its writers and verbs taking them
from there — 154 call sites away.*

**2. A panic used as control flow.** `die()` calls `exitProcess`, which in command
mode panics with `exitPanic(code)` and is recovered by the runner. It is the seam
the exit-code tests already use, which is the only thing that makes it defensible.
A verb that holds a lock or has a deferred write when it dies is unwound in ways
nobody designed. *The real fix is verbs returning errors.*

**3. The router is rebuilt for every command.** `buildRouter` runs per keystroke-
accepted line, and again inside `withSubject`, and again inside `complete`. It
allocates the whole table each time. Harmless at human typing speed, embarrassing
in a profile, and it means completion latency scales with table size.

**4. `withSubject` inspects flags by string.** It looks for a `FlagDef` whose
`Long == "id"` and appends `--id <subject>`. A verb that names its target
differently is silently not helped.

**5. The tokenizer is not a shell and pretends to be one.** Quotes and backslash,
no expansion, no `$`, no globbing. `:send -- some "quoted thing"` works;
`:send -- don't` loses the apostrophe's tail. It needs a documented grammar and a
test, and has neither.

**6. Output capture is line-based and lossy.** ANSI from a verb survives into the
panel unmeasured, so a colorized `ls` can overflow the width calculation. The
panel clips to a third of the pane and says how many lines it dropped, which is
honest but not useful — *"run it at a shell"* is the escape hatch, and a pager
that tells you to leave it is a pager that failed.

**7. `:` still lives in `transcript_jump.go` under the field name `inJump`.** The
box is a command line now; the file, the mode (`modeJump`), the help id
(`helpJump`) and the accept function (`jumpAccept`) all still say jump. Renaming
touches the keymap oracles, so I left it — but every reader after me pays.

**8. The completion candidates panel steals the panel slot.** If a command's
output panel is open and you press Tab, one replaces the other. They should
stack, or completion should render under the box.

**9. No test covers any of this.** The line editor, the tokenizer, `withSubject`
and the capture path are all provable in-process with ordinary unit tests, and I
wrote none — the verification was a pty and my own eyes. The line editor
especially deserves a table test: rune motions, kill-word boundaries, history
wrap, and the UTF-8 accumulator with a rune split across two reads.

## R2.5 Next, in order

1. **Verbs return errors and take writers** — kills dodges 1, 2 and most of 6.
   Do it verb by verb behind what already works.
2. **One constructor for `interactiveInput`** (the round-1 finding) — kills the
   two-front-doors and two-owners pitfalls, and lets `:open` work in a send.
3. **Cache the router**; add a `Command` field naming an aria positional (dodge 4).
4. **Unit tests** for the editor, tokenizer and `withSubject` (dodge 9).
5. **Rename the jump box to the command line** (dodge 7), once the oracles are
   settled.
