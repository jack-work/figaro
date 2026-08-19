# Procedural UI testing: driving figaro in a real terminal

**What this file is.** A repeatable *procedure* for answering questions about
figaro's terminal UI that only a real pty can answer, generalized out of the
paint hunts. It is a method, not a bug story: phases, oracles, the traps that
belong to the method itself, and the criteria for promoting a manual sweep into
an automated test.

**Status, plainly: this is a manual, agentic process today.** Most of what
follows *could* be a Go test: §6 lists the concrete candidates and what each
one needs before it can be written. It is written down as a procedure first
because the expensive part has never been the assertion; it is knowing what to
drive, what to look at, and which "clean" results are lies. An agent handed this
file can run a hunt end to end; a test suite cannot yet.

### The four neighbours, and who owns what

| artifact | owns |
|---|---|
| **tmux-testing skill** (`~/.config/figaro/skills/tmux-testing.md`) | the ELEVEN ENVIRONMENT TRAPS: pane heights, `-e PATH` being ignored, counting tokens, capture vs scrollback. Read it first; it is why each step below is shaped the way it is. |
| **`docs/paint-repro.md`** | one subsystem's cookbook: the pager's geometry, the store fixtures, and the findings of the paint hunts (§8.1, §8.4). |
| **`scripts/paintpane.sh`** + `paint-jogdiff.sh`, `paint-gapcheck.sh` | the runnable shell harness (`pp_*` verbs) and two packaged oracles. |
| **`internal/cli/tmuxsmoke_*_test.go`** | the automated end of the same method: `FIGARO_TMUX_SMOKE=1 go test ./internal/cli/ -count=1 -run TestSmoke`. Real binary, real pty, real provider, skipped by default. |
| **[tapes.md](../debugging/tapes.md)** | recording a live aria's wire and replaying it: the honest fixture when the bug needs a real session's shape, and the CI end of one. |
| **this file** | the PROCEDURE that stitches them together, and the promotion path from a hunt to a case. |

---

## 1. First decide whether you need a terminal at all

Reaching for tmux by reflex costs minutes per iteration and buys nothing for
most questions. Triage first:

| the question | instrument |
|---|---|
| "what does the composer decide to paint?" | unit test over `compose()` / the row builders |
| "what bytes does the painter emit for this frame?" | the VT model (`newVT` in `transcript_paint_test.go`), a real terminal parser, no pty |
| "what is on the screen after N frames and a resize?" | the VT model, still: it applies escapes and scroll regions |
| "does the process exit / does a key reach the shell / did the pager stay up?" | **tmux** |
| "did anything write outside the frame buffer?" | **tmux**, or the byte stream (§5c) |
| "does this look right to a human?" | **tmux**, and then a human |

The rule the skill gives: *a model of a terminal only knows the bugs its author
imagined.* The corollary is that the model is still the right tool when the
property is about what figaro **decided**, and the pty is the right tool when
the property is about what **happened**.

---

## 2. The procedure

Seven phases. They are in this order because each one invalidates results
gathered before it.

### P0: ISOLATE

```sh
source scripts/paintpane.sh
pp_init <name>        # stamped build + scratch store + PRIVATE tmux socket
```

Non-negotiables, each of which has cost somebody a wrong answer:

- **Build it in a DEV SHELL, not with `go build`.** `nix develop --command`
  gives the flake's binary, toolchain and dependency closure; a worktree
  `go build` gives yours. A whole night of this hunt ran on stamped scratch
  builds under Go 1.26.5 while the flake builds under 1.26.1, a difference
  that can decide whether a rendering bug reproduces at all. See
  [maintaining.md](maintaining.md) for the presets and how to isolate the
  store inside a shell.
- **If you must use `go build`, stamp it.** A plain `go build` in a *worktree*
  records no revision (Go's VCS detection needs `.git` to be a directory), so
  `--version` says `unknown` and the CLI/daemon build handshake cannot fire.
  Stamping makes a scratch build LOOK like the real one; it does not make it
  the real one.
- **Absolute paths into the pane.** `tmux new-session -e PATH=…` is silently
  ignored; a pane that runs bare `figaro` runs the INSTALLED one, and an A/B
  then compares a binary with itself.
- **Private tmux socket** (`tmux -S <scratch>/tmux.sock`). `kill-server` on the
  default socket kills the user's sessions.
- **Scratch store on `/var/tmp`**, never `/tmp` (tmpfs), never the real store.
- **Never the live daemon.** It is a strict singleton on a store flock; pointing
  a test build at the real store makes them contend, not cooperate.

### P1: STAND UP A SUBJECT

Three ways to get a pager, in increasing cost:

```sh
pp_pager <aria-id>                      # `figaro listen` + ^T: ZERO tokens, instant
<fig> send --id <aria> -l -- "<prompt>" # a live turn: the spinner animates
<fig> send --id <aria> -- "<prompt>"    # inline (incipit), no pager
```

`listen` is the workhorse: it attaches without calling `figaro.qua`, so a
fully-loaded pager costs nothing and is deterministic. Use a live turn only when
the property genuinely needs one, anything about spinners, streaming deltas,
turn-end, or interrupts.

**Getting volume cheaply.** Rows, not tokens, are what the pager pages. A dozen
small turns asking for a long numbered list gives ~1500 rendered rows for a few
thousand tokens. Copying the real aria store is a last resort: it is 130 MB of
the user's own conversations, and if you do it, `mkdir -p -m 700` first, copy no
`providers/` or `hush/`, and delete it in P7, a capture of a full pane is a
photograph of somebody's private conversation.

### P2: WRITE THE ORACLE BEFORE YOU DRIVE

An oracle is a predicate over a capture that decides *wrong*, not *different*.
Write it down before the first gesture, and hold it to two requirements:

1. **It must be able to fail.** Run it against a known-bad arm (the pre-fix
   binary, or the fix reverted) and quote the failure. An assertion that has
   never failed is not evidence.
2. **It must not be healed by its own driving.** See §4a: this is the trap that
   most often produces a confident CLEAN.

The catalogue of oracles that have earned their keep is §3.

### P3: DRIVE

```sh
pp_key C-n; pp_key d; pp_type "hello"    # keys; pp_type sends ONE BYTE AT A TIME
pp_resize 100 24                         # geometry
printf '\nSTRAY' > "$(pp_tmux display -p '#{pane_tty}')"   # a write from OUTSIDE
```

Three notes:

- **Type one character per read** when input is the subject. `send-keys -l
  "whole string"` arrives as a single read; a human does not, and a byte-vs-rune
  bug hides in the difference.
- **Test the path of somebody who does not know the affordance exists.** Every
  expert test of the composer pressed its trigger key first, and all of them
  missed the loss that happens when you just type.
- **Injecting from outside the process** is the cheapest way to prove a class of
  bug involving a second writer to the terminal, without having to provoke
  whichever internal writer does it in production. It is how the status-smear
  mechanism was finally captured (`docs/paint-repro.md` §8.4).

### P4: SETTLE

```sh
pp_stable      # poll until the pane stops changing
```

**Never sleep a fixed guess.** A model's first token can take five seconds or
fifty. Poll until two consecutive captures are identical, with a ceiling.

### P5: MEASURE

Four instruments, each seeing something the others cannot:

| instrument | shows | blind to |
|---|---|---|
| `pp_cap` (`capture-pane -p`) | the visible grid, ANSI stripped | anything that scrolled away; all styling |
| `pp_hist` (`-S -`) | the full scrollback | nothing that scrolled: this is how a frame that existed for 12 ms was photographed |
| `pp_raw` (`-e`) | the grid WITH SGR | which writer produced it |
| `script -q -f out.raw -c '<fig> …'` | **the bytes figaro actually wrote** | what the emulator did with them |

The byte stream is the court of last resort and it settles arguments in one
read: it distinguishes *"the painter composed a wrong row"* from *"the painter
composed the right row and the terminal was not where it thought"*. In the
resize hunt it took a plausible three-hour theory off the table in ninety
seconds: the frame was a clean full repaint, `CUP + EL + content` on every row,
so the composer was innocent.

### P6: JUDGE (A/B, with identities printed)

```sh
md5sum "$BEFORE" "$AFTER"        # quote BOTH in the report
```

- **Print the md5 of each arm.** Two arms that produce identical output are more
  often one binary than one bug.
- **Give each arm its own daemon.** The CLI/daemon build handshake refuses a
  mismatched pair (correctly), so an A/B that leaves the first arm's daemon
  running gets a refusal, an empty capture, and a meaningless "0 failures". Stop
  the daemon between arms (`<fig> rest`).
- **Canary the finding.** Revert the fix, re-run, quote the failure text. The
  quoted failure belongs in the commit message; a good one is a miniature of the
  bug (`got "status ⠋ · ctx 1k", want "───── rule"`).

### P7: TEAR DOWN

```sh
pp_down          # tmux server on OUR socket + the scratch daemon
pp_verify_clean  # asserts both, and the store copy
```

`kill-server` alone leaves the scratch daemon running; seventeen agents each
leaving one produced 230 orphaned processes and a memory-pressure alert. Delete
any copy of the real store. Report what you actually verified, not what the
teardown script intended.

---

## 3. The oracle catalogue

Each of these has caught a real bug. `✎` marks the ones a Go test could assert
today (§6).

**a. Jog and diff** (`scripts/paint-jogdiff.sh`, `paint-repro.md` §5).
Capture, move the viewport away and back to the same offset, capture, diff. Any
difference means the first frame was a lie. Needs no model of correct content,
which is its power. **Gate it on the footer range being identical in both
captures**, a page landing between them changes the row space, and comparing
two different windows is a meaningless diff that looks like a finding.

**b. Comparison-free invariants** (`scripts/paint-gapcheck.sh`). Jog-and-diff is
blind to damage the jog itself repairs. A structural predicate over a single
capture is not: *a separator row must be blank*, *no row may exceed the pane
width*, *the status line may appear on exactly one row*. ✎

**c. Uniqueness of chrome.** The footer, the rule, the status line each appear
exactly once. Counting a token across the whole capture is unsound (the mantra
echoes the prompt; an 80-column shell echo wraps and its tail matches), so
anchor the pattern to the row shape. ✎

**d. Width invariant.** No rendered row exceeds the pane width; the widest row
should equal it exactly on a full-width frame. This is what proved `clipToWidth`
was holding and sent a hunt to the second branch of its brief. ✎

**e. Liveness, a PROCESS question.** "The chrome is gone" is not evidence that
figaro died; a pane below four rows makes the painter skip the frame on purpose,
so the screen holds tmux's truncation of an older one. Ask `pgrep`. Getting this
wrong produced 29 false failures in one fuzz run.

**f. Order on screen vs `fig show`.** The node order painted must match the
committed order the CLI reports. A live-vs-committed divergence is forbidden by
the purity invariant, and this is how a steer hoisted above its tools was found.

**g. Idempotence under repaint.** Paint frame N twice; the second must emit no
row updates. Cheap, and it catches "the composer is not a pure function of
state". ✎

---

## 4. Traps of the METHOD

The skill's eleven traps are about the environment. These five are about the
procedure, and each cost a measured wrong answer during the resize-smear hunt.

**a. An instrument whose gestures heal what it hunts.** The randomized resize
fuzz reports 0 failures against a *provably broken* binary: because `resize()`
discards the painter's model of the screen, which is exactly the repair the bug
needs. The fuzz is a guard on invariants; the discriminating instrument was a
script that injects damage and then **holds still**. Before trusting a sweep,
ask what its own driving repairs.

**b. Scoring the unmeasurable.** A pane under 10 rows cannot show the pager's
chrome, and under 4 the frame is deliberately skipped. Drive those sizes: the
invariant is that the *next workable size* is clean: but do not score them.
Scoring the unmeasurable manufactures false alarms; the mirror image, a fixture
whose footer shows no range, manufactures false calm (with `maxOff == 0` every
navigation key is a no-op, so a jog-diff compares a frame with itself and prints
CLEAN for any binary, including a provably broken one). **Gate on the fixture
being able to fail.**

**c. Gestures leak to the shell.** When the subject exits, a turn ending, a
crash: subsequent `send-keys` land in bash. `C-d` is EOF, which kills the
session, which kills the server, which looks exactly like the crash you were
hunting. Keep `C-d` out of fuzz gesture sets and stop the sweep when the subject
is gone.

**d. The build handshake between arms.** See P6. It is a *feature*: the daemon
refuses to speak a different build's wire: but in an A/B it presents as an
empty capture and a clean score.

**e. Symptom-shaped hypotheses.** The user reports a bug "when resizing", so the
hunt goes to the resize path: where, measured, it is clean, and the resize turns
out to be the *cure*. Do the negative measurement early and write it down; two
recorded negatives ("the resize repaint is clean", "the shrinking status line is
correct elision") are worth more to the next hunter than the positive, because
they are where the day goes.

---

## 5. What a hunt hands back

A report that has to be re-verified is worth less than a shorter one that can be
trusted. Report only what has a return value or a file behind it, and where you
intend something, say INTEND.

1. absolute worktree path; branch @ short sha
2. **the repro recipe**: exact geometry, subject size, key sequence. This is
   the deliverable that matters most, and it is worth writing down *before* the
   fix exists.
3. root cause, one paragraph
4. what changed
5. the A/B table with both md5s
6. the canary failure, quoted
7. **what remains unproven**: including anything you decided not to decide
8. hygiene: processes, sockets, store copies

---

## 6. Ought to be test cases (and what each one needs)

**The honest state of things.** Everything above is run by hand, or by an agent
following it. That is not where it should stay. Sorted by how ready each is:

**Ready today: VT model, no pty, no provider.** These are ordinary Go tests
against `newVT`; the only reason they do not all exist is that nobody has
written them.

- *the painter's model can be void or stale*, an unannounced scroll of the VT
  followed by a frame must not leave stale rows once the painter is told
  (`internal/cli/transcript_screenmoved_test.go`, on `fix/resize-bleed`, is the
  first of these).
- *width invariant* (§3d) over a matrix of widths × content shapes.
- *chrome uniqueness* (§3c) on any composed frame.
- *idempotence under repaint* (§3g).
- *ellipsis and shed order* for the status row at narrow widths.

**Ready with work: the existing tmux smoke suite.** `TestSmoke_*` already
drives a real pty and a real provider behind `FIGARO_TMUX_SMOKE=1`. Candidates
that fit its shape, each naming the bug it would have caught:

- `TestSmoke_ResizeSweepIsClean`: jog-and-diff across a width matrix, with the
  range gate of §3a. Needs a **deterministic fixture with a real range**, which
  is precisely what the paint hunts found `pp_fixture` lacking.
- `TestSmoke_StrayWriteDoesNotSmear`: inject to `#{pane_tty}`, hold still,
  assert one status row. Needs only what `scripts/paint-strayscroll.sh` already
  does (on `fix/resize-bleed`, with `scripts/paint-fuzz.sh`; neither is on main
  yet).
- `TestSmoke_TinyPaneRecovers`: shrink below the pager's chrome and back;
  assert the next workable size is clean. Needs the "unmeasurable" gate of §4b
  expressed as a skip.

**Resists automation, and should stay a procedure.** Say so rather than write a
weak test:

- *"does this look right"*: kerning, dimming, whether a footer reads as chrome
  or as content. A human, or a screenshot review.
- *the discriminating power of a new oracle*: §2 P2's first requirement is a
  judgement call about a known-bad arm that does not exist yet.
- *exploratory fuzzing*: its value is finding the gesture nobody thought of,
  which is the part a fixed case cannot contain. Fuzz by hand, then **promote
  each finding into a fixed case**; that promotion is the whole point.

**Promotion criteria**, a manual sweep becomes a test case when all four hold:
it has a fixture that can fail (§4b); an oracle that failed at least once on a
known-bad arm; a bounded runtime; and a cleanup path that leaves no daemon,
socket, or store copy behind. Until then it belongs here, as a procedure, run by
somebody: or something: that can read.

---

## 7. Worked example, end to end

The status-bar smear (`docs/paint-repro.md` §8.4), in the phases above:

```
P0  scratch store on /var/tmp, private socket, stamped build
P1  `figaro listen <aria>` + ^T          (zero tokens), then a live turn for the spinner
P2  oracle: the status line may appear on exactly ONE row      (§3c)
P3  resize sweep -> CLEAN. Negative result, recorded.
    byte stream -> a full, correct repaint. Composer exonerated. (§5 P5)
    inject `printf '\nSTRAY' > $(tmux display -p '#{pane_tty}')`, then HOLD STILL
P4  pp_stable
P5  1 status row -> 2, still 2 after 4s, back to 1 after a resize
P6  A/B: 5a37544349f5 = 2 rows persisting; 2fafddf45911 = 1. Canary: revert the
    fix and the unit test prints `got "status ⠋ · ctx 1k", want "───── rule"`.
P7  pp_down; store copy deleted; verified by measurement, not by intent
```

The whole hunt is four commands and one injection. Finding *which* four took a
day, which is what this file is for.

---

*Ogni cosa misurata, niente indovinato.*
