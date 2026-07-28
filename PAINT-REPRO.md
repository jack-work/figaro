# PAINT-REPRO.md — a cookbook for hunting figaro's transcript-pager paint bugs

Written by ALMAVIVA in Phase 1 so BASILIO, BARTOLO and CHERUBINO do not each
have to rediscover the same eleven traps. **Everything in here was measured on
this machine, not reasoned about.** Where I state a number, I measured it; where
I state a mechanism, I have a capture. Where I am guessing, it says so.

Companion file: `scripts/paintpane.sh` (source it; every function cites the trap
it exists to defend against).

---

## 0. The headline: you can drive the pager for ZERO tokens

`figaro listen <aria-id>` attaches to an aria **without calling `figaro.qua`** —
no prompt, no provider, no tokens, no auth. `Ctrl-T` then promotes to the
transcript pager. If the aria already has hundreds of messages, you have a
fully-loaded pager in about four seconds.

To get hundreds of messages without spending anything, **copy the real aria
store into a scratch store**. The real store is only ever read:

```sh
cp -r ~/.local/state/figaro/arias  /var/tmp/paint-<hunter>/state/arias
cp -r ~/.config/figaro/.           /var/tmp/paint-<hunter>/config/
```

Measured: 119 MB, ~20 s, **305 arias**. Good fat ones in that store:

| aria | msgs | why it is useful |
|---|---|---|
| `8566c903` | 561 (1058 rendered rows) | tool-heavy, prose-heavy, wide diffs — the one I used for every capture below |
| `e618d51f` | 162 | mid-size |
| `12299407` | 96 | small, fast to reach a floor |

`pp_seed` in the harness does this. Put the scratch store on **`/var/tmp`
(disk)**, not `/tmp` (tmpfs, 28 G shared with everything) — trap #10's
memory-pressure incident was 1.2 GB of tmpfs.

Use the real provider only when the bug genuinely needs a *live turn*
(CHERUBINO probably does: an error bleeding into the status bar wants something
to go wrong mid-stream). For anything about scrolling, resizing, gaps or
selection, `listen` is strictly better: free, instant, and deterministic.

---

> ### ⚠ `paint-jogdiff.sh` SPENDS A PROVIDER TURN IF YOU ASK IT TO MINT
>
> Only then, and it is **gated** — not merely documented. CHERUBINO caught the
> footgun, which was mine: `pp_env` resolves `FIGARO_CONFIG_DIR` to
> `${PP_CONFIG:-$HOME/.config/figaro}` — **the real config, by reference** — and
> `pp_fixture` calls `figaro new`. So `paint-jogdiff.sh <hunter> -` would have
> resolved the master's **real credentials** and spent a **real provider turn**,
> *silently*, as a side effect of a script whose name says "jogdiff". A stand-down
> would then have been **one unset variable** away from being violated by whoever
> ran the obvious command.
>
> He proposed a line in this file. **A guard beats a doc line** — a document
> stating an intent the code does not enforce is the exact family we spent the
> night auditing — so minting now refuses unless you opt in, and the refusal tells
> you how to proceed:
>
> ```sh
> scripts/paint-jogdiff.sh <hunter> <aria-id>          # free: uses existing content
> PP_ALLOW_TURN=1 scripts/paint-jogdiff.sh <hunter> -  # SPENDS A TURN, deliberately
> ```
>
> Measured: without `PP_ALLOW_TURN` it exits **2** having spent nothing; with an
> explicit aria id it never reaches the guard.

## 1. Naming and cleanup contract (agreed with BERTA, the watchdog)

> ### ⚠ STANDING ORDER — REPORTING
>
> **Report only what has a return value or a file on disk behind it. Where you
> intend something, say INTEND.**
>
> ROSINA adopted this fleet-wide after I broke it four times in one night: I told
> the watchdog I had removed a socket I had never removed; I reported a directory
> as credential-exposed having measured only that its *path* existed, never its
> mode; I reported `MERGE-REPORT.md` to my parent before committing it, so she
> looked and it was not there; and I told her three relays were *executed* while
> they were still sentences I had composed and not sent.
>
> It is the same defect as `pgrep -x tmux` reporting CLEAN over a field of
> orphans — **a convenient proxy substituted for the thing that actually decides
> the property, failing toward the comfortable answer.** Those proxies fail toward
> *clean*; this one fails toward **done**. It is the sixth instance of the class in
> `skills-patch-trap12.md` §A, and the only one that is an *agent* rather than a
> tool.
>
> Nothing was corrupted, because my parent re-measured every load-bearing claim
> before it reached the master. That is the method working, not luck — and it is
> exactly why the habit matters: **a fleet that needs its parent to re-measure
> every relay does not scale past one parent.**
>
> Duller reports are the correct trade. A report that has to be verified is worth
> less than a shorter one that can be trusted.

Violate this and a sweeper cannot tell your processes from the user's.

> ### ⚠ NEVER RELY ON A PARENT DIRECTORY TO PROTECT A FILE YOU ARE ABOUT TO MOVE
>
> A real incident in this very operation, found by BERTA mid-run, and the most
> expensive mistake in this file.
>
> **The controlled experiment, from SUSANNA — quote this pair, not the single
> case.** Two secrets were copied into the *same* four 755 directories in the
> *same* minute by the *same* `cp`:
>
> | file | its own mode | outcome |
> |---|---|---|
> | `providers/anthropic.toml` | **644** | **EXPOSED** — world-readable in a 1777 tree |
> | `hush/identity.age` (an AGE **private key**) | **600** | **safe** |
>
> One variable. **Its location saved nothing; its own mode saved everything.**
> `cp` preserved modes faithfully in both cases — that was never the problem. The
> problem is that `anthropic.toml` was safe in the real config *only because its
> parent is 700*, and copying it out from under that parent left the master's
> Anthropic credential world-readable inside `/var/tmp`, which is `1777`,
> world-traversable, **and survives reboot**. Four copies. Alongside four 119 MB
> copies of his real aria store — his actual conversation history.
>
> *A rule with a negative control is a rule someone will actually follow.*
>
> It is the trap #10 family one turn crueller: not an artifact outliving its
> process, but **a secret outliving its shield**. And the durability that made
> `/var/tmp` the right choice over tmpfs is exactly what makes it the wrong place
> to leave one.
>
> ### ⚠ AND: A COPY THAT SILENTLY REACHES BACK INTO THE ORIGINAL
>
> Same family, different mechanism, also SUSANNA: **`cp -r` copies a symlink as a
> symlink**, so an "isolated" scratch config is **not hermetic**. Hers had
> `skills/plaid` and `skills/pishot.md` pointing back into the master's *live*
> `~/dev` trees — an arm that believed it was reading an isolated skill was reading
> the live file, and a test that believed it was hermetic was not. Verified here:
> `~/.config/figaro/skills` contains exactly those two links today.
>
> Use **`cp -rL`** (or `tar -h`) wherever an arm needs real isolation, **and then
> assert no symlink survived** — a hermeticity claim is worth nothing unchecked.
> `pp_config_copy` now dereferences and refuses to proceed if it finds a link.
>
> She found it only because her own audit threw a **false positive** on symlink
> modes (777, unsettable) and she *checked* instead of "fixing" it — the third time
> in one night a diagnostic was right for a reason its author did not expect.
>
> **Not an exposure but still wrong:** a 0600 copy of a private key in a
> reboot-surviving directory is not a leak, and **the correct number of them is
> still zero.** Measured across every tree under my control: zero `*.age`, zero
> `hush/`.
>
> **What the harness does now. Do not undo any of it.**
>
> | concern | how it is handled |
> |---|---|
> | credentials | **never copied.** `FIGARO_CONFIG_DIR` is **shared by reference** to the real config — the documented `.#share-config` shape (isolate runtime+state, share config). A reference cannot be left behind with the wrong mode, so sharing does not reduce the risk, it **deletes the failure mode**. |
> | isolated config | `pp_config_copy`, if you truly need one. **Excludes `providers/` and `hush/`**, then *refuses to continue* if any group/world-readable file survived. Auth comes from the environment instead. |
> | content | `pp_fixture N` — **synthetic, not the master's history** (below). |
> | dirs | `mkdir -p -m 700` **before anything lands**. Do not trust umask; this box yields 755. |
> | `/tmp/paint-<hunter>/` | **also 700.** BASILIO caught this after I had declared victory: I fixed `/var/tmp` and left `/tmp/paint-*` at **755 with 99 world-readable capture files** — and a jogdiff capture is *a photograph of the master's conversation*. BERTA's glob covered the path, not the **mode**. Closed, captures deleted; `pp_fixture` supersedes them. |
> | teardown | `pp_down` `rm -rf`s the config copy **first**, then the store. |
> | `pp_seed` | **disarmed** — prints why and exits 1 rather than silently no-op'ing, because three hunters had already been told to call it. |
>
> Measured, so the change need not worry you: with `providers/` and `hush/`
> deleted, `figaro list -g -a` still lists all 305 arias and the zero-token pager
> works normally. **Nothing the painters do needs the master's credentials.** Only
> a turn that reaches a provider does, and the cheapest way to get one is a
> deliberately garbage `ANTHROPIC_API_KEY` in the environment — which needs no
> config at all (CHERUBINO's find; §8.3).

**`pp_fixture` — synthetic content, and BETTER than real history for this job.**
One tool call emitting `seq 1 N`, so the pager holds N strictly increasing
numbered rows. Contamination becomes self-evident with no oracle at all: a row
reading `247` where `312` belongs is visibly wrong, and **a gap row containing
anything whatsoever is a bug, because every legitimate body row is a bare
number.** Cost is one cheap turn — the N rows are *tool output*, not model output,
so the model emits only a call and a word.

```sh
pp_init basilio
ARIA=$(pp_fixture 400)     # deterministic, no privacy exposure
pp_up 100 40 rz
pp_pager "$ARIA"
```

```
tmux socket   /tmp/paint-<hunter>/tmux.sock     PRIVATE server, never the default socket
tmux session  paint-<hunter>-<tag>
scratch store /var/tmp/paint-<hunter>/{state,run,config}
binary        /tmp/paint-<hunter>/figaro        stamped, invoked by ABSOLUTE PATH
hunters       alma | basilio | bartolo | cherubino
```

A **private socket** is the whole defence: `kill-server` on your own socket
provably cannot touch the user's sessions (`0 dev figaro-qua fx gw4 iq iq2` live
on the default socket). Never run bare `tmux kill-server`.

**`/tmp` is RAM.** It is a 28 G tmpfs shared with everything on the box, and a
stamped figaro is ~38 MB. `pp_init` always builds to the same path (`-o
"$PP_BIN"`) so *rebuilds overwrite and do not accumulate* — but **A/B variants
do**: keep `figaro`, the arm you drive interactively, in `/tmp/paint-<hunter>/`,
and put probe/fixed/variant binaries in `/var/tmp/paint-<hunter>/` (disk, 703 G
free). Four hunters × one binary is ~154 MB of RAM; four hunters × three arms
each is not. Record each arm's `md5sum` in your write-up either way — that is the
evidence, not the file's location.

**No nix dev shells.** A dev root is shared; a scratch store is not. So there is
no `FIGARO_DEV_ROOT` to hand a sweeper — instead a daemon is attributable by env:

```sh
for p in $(pgrep -x figaro); do
  tr '\0' '\n' < /proc/$p/environ | grep -q "^FIGARO_RUNTIME_DIR=/var/tmp/paint-" \
    && echo "$p is a hunter's"
done
```

Teardown is **two halves** (trap #10 — `kill-server` leaves the daemon running;
seventeen agents once left 230 orphaned processes):

```sh
FIGARO_STATE_DIR=… FIGARO_RUNTIME_DIR=… FIGARO_CONFIG_DIR=… /tmp/paint-<h>/figaro stop --force
tmux -S /tmp/paint-<h>/tmux.sock kill-server
```

`pp_down` does both and is installed as an **EXIT trap** by `pp_init`, so an
aborted script still cleans up. Not-ours, do **not** reap: daemons with
`FIGARO_RUNTIME_DIR` unset (the live one), and anything under
`/run/user/1000/figaro-dev-share-hush/run`.

**A dead tmux server leaves a live-looking socket.** Found by BERTA mid-run:
`kill-server` removes the server but leaves the socket *inode*, so

```sh
[ -S "$sock" ] && echo "server is up"      # WRONG: says up for a dead server
tmux -S "$sock" has-session 2>/dev/null    # right: ask tmux, not the filesystem
```

Same family as trap #10 — **the artifact outlives the process.** `pp_alive` /
`pp_server_alive` do it properly and `pp_down` now `rm -f`s the socket. If you
wrote your own liveness probe as a file test, fix it.

**`pp_seed` copies the CONFIG too, and that is not optional.** It does two
unrelated things: the aria store (content to page through) *and*
`~/.config/figaro` → `$FIGARO_CONFIG_DIR` (loadouts and provider credentials).
Skip it and `FIGARO_CONFIG_DIR` points at an empty directory, so you get no
loadouts and no credentials — a fresh turn then fails on the **first-run /
missing-credential** path. Two consequences:

- `figaro listen` against an unseeded store shows you a confusing *nothing*.
- Worse for CHERUBINO specifically: an unseeded config makes a turn die with
  "no credential", which is exactly the branch that reaches
  `providerSetupHint()` at `stream.go:167`. You would photograph a real bleed
  **for a reason you did not choose**, and mistake an accident for a repro. If
  you are testing the error path, decide *which* error you are provoking and
  make it deliberate.


---

## 2. Stamp the binary. Always.

```sh
go build -ldflags "-X github.com/jack-work/figaro/internal/cli.commit=$(git rev-parse --short=12 HEAD)" \
  -o /tmp/paint-<hunter>/figaro ./cmd/figaro
```

A plain `go build` **in a worktree records no revision at all** — Go's VCS
autodetection only fires when `.git` is a directory, and a worktree's is a file.
`-buildvcs=true` neither helps nor complains. Then `figaro --version` says
`unknown` and the CLI/daemon build handshake has nothing to compare.

**An A/B must not share a daemon between arms.** I got this wrong and the binary
caught me, which is the stamp earning its keep. The scratch daemon spawned by
arm A keeps running; arm B's CLI then refuses outright:

```
error: running angelus is a different build than this CLI:
  daemon 5069adfe5b48
  cli    probe-5069ad
the wire changes between builds, so this pair would render nothing.
```

So **`figaro stop --force` (with the scratch env) between arms**, and treat a
silent pass here with suspicion: had I not stamped, both arms would have said
`unknown`, the handshake would have had nothing to compare, it could only have
warned — and arm B would have rendered through a mismatched wire instead of
failing loudly. That is precisely how an old daemon once rendered a user's own
question in figaro's voice, four times, with no error.

**And print the identity of every arm** (trap #11). `tmux new-session -e PATH=…`
is *silently ignored*, so an A/B done that way runs the **same binary twice** and
reports "the fix had no effect". Two defences, use both: invoke by absolute path,
and print `md5sum` per arm in the report. My own A/B below shows
`md5=869ce2c84543` for *before* and a different one for *after* — that line is
not decoration, it is the evidence that the arms differed.

---

## 3. Three environment facts that will otherwise cost you an hour

**(a) `FIGARO_ARIA` leaks into the pane and is an identity.** An aria's bash tool
exports `FIGARO_ARIA=<own id>` and `FIGARO_NO_BIND=1`. Inherited into a test
pane, every `figaro list` scopes to *the hunting aria* and every `figaro send`
talks to itself. **Measured:** `figaro list -a` against a seeded 305-aria store
returned exactly **one** row until I unset them. Always:

```sh
unset FIGARO_ARIA FIGARO_NO_BIND      # inside the pane's shell
figaro list -g -a                     # -g/--global, or you see only your subtree
```

**(b) Pane height is `-y N` → `N-1`** (trap #1). The status bar takes a row and
turning it off afterwards does **not** give it back to a detached session, nor
does `resize-window`. **Measured here:** `-y 41` → 40; `resize-window -y 25` →
24. So ask for `h+1`, then read back `#{pane_height}` and assert against *that*.
An entire thread of "h=1 loses the reply" was measured at pane height **zero**.

**(c) Inside the pager there is NO SCROLLBACK.** The pager enters the alternate
screen (`enter()` emits `altScreenOn + autowrapOff + mouse + cursorHide +
\x1b[2J`). **Measured while the pager was up:** `alternate_on=1`,
`history_size=0`, and `capture-pane -p -S -` returned exactly the same 40 rows
as `capture-pane -p`.

> So trap #4 ("capture scrollback, not the pane") **does not apply inside the
> transcript pager.** There is nothing to capture. A pager bug must be caught on
> the live grid. Trap #4 still applies to the *inline/incipit* view and to
> whatever is left in history after `q` drops you out of the pager.

This is why the oracle in §5 exists: with no history to photograph, the only way
to catch a transient frame is to have something to compare it *against*.

---

## 4. Stand up, drive, resize, tear down

```sh
source scripts/paintpane.sh
pp_init basilio           # stamped build + scratch dirs + EXIT trap; prints identity
pp_seed                   # copy the real store in (once; idempotent)
pp_up 100 40 rz           # pane of EXACTLY 40 usable rows, aria identity scrubbed
pp_pager 8566c903         # listen + ^T, waits for chrome, ~4 s

pp_key g; pp_key g        # gg -> top of the held window (deterministic start)
pp_key d                  # half-page down   (u = up, j/k = one row, G = bottom)
pp_key C-n                # select next node (C-p previous)
pp_key Enter              # expand the selected tool
pp_key C-o                # toggle verbose tool inputs
pp_stable                 # poll until the pane stops changing (NEVER sleep a guess)

pp_resize 100 24          # the resize under test (window resize, like a user's)
pp_cap  > a.txt           # visible, escapes stripped
pp_raw  > a.esc           # visible, escapes KEPT — use this for SGR/status-bar work
pp_hist > a.hist          # full scrollback (== pp_cap inside the pager; see §3c)
pp_down                   # both halves
```

Keys the pager takes (from `transcript.dispatch`): `j/k` row, `u/d` half page,
`gg/G` ends, `C-n/C-p` node selection, `Enter` expand, `C-o` verbose, `/` search
with `n/N`, `!` status panel, `?` help, `q`/`Esc` leave, wheel scrolls natively.

**Type input one character at a time** (trap #5) with `pp_type`. `send-keys -l
"whole string"` arrives as a single read; a human does not. That distinction hid
a byte-vs-rune bug that mojibaked every non-ASCII character. Relevant to
`/`-search here.

---

## 5. The oracle: JOG AND DIFF. Use this, not your judgement.

The user's own words for the gap bug are also the test:

> "It can only be cleared by moving the viewport such that the corrupt region is
> no longer visible, and then moving back to that area. It will typically be
> fixed upon return."

So: **capture the suspect frame, move the viewport far away, come back to the
same offset, capture again, and diff.** If they differ, the first frame was a
lie. This is self-validating — it needs no model of what the content *should*
be, which is exactly what trap #9 ("verify the model complied before blaming the
renderer") and the *"every double diverged by being tidier than reality"* rule
are warning you about. I nearly reported three legitimate-looking prose rows as
corruption before running it.

```sh
pp_cap > suspect.txt
for i in $(seq 1 6); do pp_key u; sleep 0.15; done; pp_stable
for i in $(seq 1 6); do pp_key d; sleep 0.15; done; pp_stable
pp_cap > truth.txt
diff -u suspect.txt truth.txt        # any output at the SAME footer range = a bug
```

Assert the footer range matches in both (`· 219–240/1058+`) — if the jog did not
land back on the same offset you are diffing two different viewports and the
result means nothing. Keep away from the very bottom, where a downward jog
re-attaches to `live` and changes the offset.

Other honest-assertion rules that apply here:

- **Gate every absence on chrome** (trap #3): `pp_chrome` must be `>0` before you
  believe you are in the pager, and `==0` before you believe an absence measured
  outside it. (Note `pp_chrome` counts *lines* containing `? help`/`! status`;
  the pager puts both on one row, so a healthy pager scores **1**, not 2.)
- **Compare sequences, not neighbours** (trap #8): `pp_dupruns file [n]` reports
  repeated runs of ≥n non-blank rows. The body-duplication bug put its two
  copies ~25 lines apart with re-rendered thinking in between; an adjacent-line
  check would have missed it.
- **Never sleep a fixed guess** (trap #7): `pp_stable`.

---

## 6. Measured terminal behaviour you must design around

**An alt-screen shrink is not a clip — it is a SHIFT, and its size depends on
where the cursor is.** I probed tmux 3.x directly, painting `ROW01..ROW20` into
an alt screen with autowrap off and shrinking 20 → 12 rows, with no repaint by
the application:

| cursor when the resize lands | what tmux keeps |
|---|---|
| top (`\033[1;1H`) | `ROW01..ROW12` — deleted 8 from the **bottom** |
| bottom (after painting row 20) | `ROW09..ROW20` — deleted 8 from the **TOP**, content shifted **up 8** |

The second row of that table is the operational one, because figaro's painter
finishes every frame by writing the status row at `screen[t.h-1]` and so leaves
the cursor at the bottom. **Therefore: every shrink of the pager shifts the
terminal's grid upward by `min(rows_lost, cursor_y)`, and the painter's memory of
the screen does not move with it.** Reproduce with the probe in
`/tmp/paint-alma/` style — 15 lines of `printf`, no figaro involved. Do that
before blaming figaro for anything resize-shaped.

---

## 7. The in-repo instruments — often the right tool, and free

Do not reach for tmux when a deterministic harness will do. Reproduce in the VT
harness *before* fixing: a pty capture proves the bug is real, the harness gives
you a regression test insulated from capture artifacts.

| file | what it is |
|---|---|
| `internal/cli/transcript_paint_test.go` | **the VT harness lives here** (`newVT(w,h)`, `vtScreen`, `assertSameGrid`, `teeVT`, `scrollTranscript`) — *not* in a `vt_test.go`; the brief's path is stale. `naivePaint` is the reference painter to diff against. |
| `internal/cli/transcript_paint_tmux_test.go` | replays the pager's real escape stream into tmux and compares to `t.prev`. **This is the test that already catches "the painter's belief ≠ the screen".** Extend it; it is the closest thing to the three bugs. |
| `internal/cli/transcript_frames_golden_test.go` | frame goldens |
| `internal/cli/tmuxsmoke_test.go` + `_cases_test.go` | `FIGARO_TMUX_SMOKE=1 go test ./internal/cli/ -run TestSmoke -v` — real binary, real pty, **real provider** (costs tokens). Read the case comments: each names the shipped bug it exists to catch. |

Useful existing helpers to imitate rather than rewrite: `newPane`/`waitIdle`/
`pagerChrome`/`bodyLines`/`footers`/`close` in `tmuxsmoke_test.go`, and
`recordScrollSession`/`tmuxReplay`/`visibleText` in the tmux replay test.

**`transcriptScrollRegions` is an escape hatch:** `FIGARO_NO_SCROLL_REGION=1`
disables the DECSTBM/SU/SD path. A/B with it is the fastest way to decide whether
a bug lives in `planScroll` or in the plain diff. (Both test suites already
parametrise on it — copy that shape.)

---

## 8. What I already found, so you do not spend a day on it

**BASILIO and BARTOLO: I believe you are ONE BUG.** ROSINA guessed it; here is
the evidence. Do not ship two fixes for it — read §9.

### 8.1 Gesture sweep: the bug is RESIZE-ONLY

I ran the §5 oracle after each gesture in turn, same pane, same aria. `divergent`
is the number of rows where the on-screen frame disagreed with a freshly painted
one at the *same* offset:

| gesture | divergent | widest row |
|---|---|---|
| baseline, no gesture | **0** | 100/100 |
| `C-n` ×3 (node selection) | **0** | 100/100 |
| `Enter` (expand tool) | **0** | 100/100 |
| `C-o` on (verbose) | **0** | 100/100 |
| `C-o` off | **0** | 100/100 |
| **width 100→72, height unchanged** | **5** | 72/72 |
| **width 72→120, height unchanged** | **3** | 120/120 |
| height 40→56 | *(oracle refused — see below)* | |
| **both 120×56 → 64×20** | **1** | 64/64 |

Three conclusions, and one of them corrects me:

1. **Scrolling, selection, expansion and the verbosity toggle are clean.** The
   contamination is not spontaneous and not gesture-driven — it needs a resize.
   So BARTOLO's "gaps populated with text from some other line" is *reached*
   through BASILIO's trigger. That is the convergence argument, measured.
2. **A WIDTH-ONLY change is enough.** This is the better repro — simpler, and it
   **falsifies any explanation that depends on the vertical shift in §6.** No rows
   move; tmux merely truncates them. The blank-row skip in `paint` is therefore
   *sufficient on its own*: at 72 columns the old 100-column text is truncated in
   place, every non-blank row is repainted, and every blank row is skipped and
   keeps its truncated leftovers. Treat §6 as an **amplifier that makes the
   symptom uglier, not a necessary cause.** I had it the wrong way round in my
   first draft; the sweep corrected me.
3. **`clipToWidth` is holding.** `widest_row` equalled the viewport width exactly
   in every single frame and never exceeded it. That is a useful *negative*
   result for CHERUBINO: on this evidence the status-bar bleed is unlikely to be
   an over-wide body row, which points at the other branch of the brief's fork —
   something writing to stdout/stderr outside the frame buffer. Not proof; I
   never provoked an error.

**The height-growth caveat, and why the oracle refused.** Growing 40→56 changed
the footer total from `1028+` to `1212+`: a taller viewport asks for more history,
a page lands, and the row space itself grows. The jog then returns to a *different*
viewport (`212–265` vs `394–447`), so the diff would have been meaningless. My
script detects this and prints `SKIP` rather than a number — **keep that gate.**
A silent comparison there is exactly the kind of tidy-looking false result this
whole file is written against. To test height growth you need a different oracle
(resize back and re-diff, or pin the window first).

### 8.2 The original repro

**Repro** (exact, reproduced in both A/B arms):

```sh
pp_init alma; pp_seed; pp_up 100 40 rz; pp_pager 8566c903
pp_key g; pp_key g; for i in 1 2 3; do pp_key d; sleep .3; done; pp_stable
pp_resize 100 24; pp_stable
pp_cap > suspect.txt          # then JOG AND DIFF per §5
```

**Capture** — same footer range `· 219–240/1058+` in both, three rows that must
be blank instead hold text from a *different* line of the taller frame:

```
-   currently puts the  seal  (the dim rule) between every message. With the header now above the
+
-                           ← blank
+
-     [next user body]
+
```

That is BARTOLO's report verbatim ("the gaps in between nodes are populated with
text that shouldn't be there from some other line"), produced by BASILIO's
trigger (a resize), and cleared by exactly the gesture the user described.

**Root cause — `transcript.paint`, `internal/cli/transcript.go:1534`.** `resize`
(`transcript.go:978`) sets

```go
t.prev = nil   // full repaint (diff vs nil); no \x1b[2J, which flickers
```

but **diff-vs-nil is not a full repaint.** `paint` reads the base row as

```go
var old string
if r < len(base) { old = base[r] }
if screen[r] == old { continue }
```

With `base == nil`, `old` is `""` for every row — so every row whose new content
is the **empty string is compared equal to the base and skipped entirely**. And
blank rows are everywhere: `entryLine` returns `""` for row 0 of every message
separator (`transcript_index.go`, `case 0: return ""`). Meanwhile §6 says the
terminal has just *shifted its whole grid upward*. So each separator's blank row
keeps whatever text the shift slid into it, and stays wrong until the viewport
moves enough to make that row differ from `t.prev` — "typically fixed upon
return". The comment on that line states an intent the code does not implement:
it suppresses `\x1b[2J` to avoid flicker and then relies on a diff that cannot
see blankness.

**Verification (A/B, both arms driven identically, identities printed):**

| arm | binary | md5 | result |
|---|---|---|---|
| before | `/tmp/paint-alma/figaro` | `869ce2c84543` | **CONTAMINATED** — 3 divergent rows |
| after | `/tmp/paint-alma/figaro-probe` | *(differs; printed by the script)* | see §9 |

The probe patch — a *probe, not a proposed fix* — makes a nil base mean "repaint
everything":

```go
base := t.prev
full := base == nil
if plan, ok := t.planScroll(screen); ok { …; full = false }
…
if !full && screen[r] == old { continue }
```

**paint/base deliberately contains NO fix.** I built the probe, ran it, and
reverted the worktree (`git checkout -- internal/cli/transcript.go`). The branch
you fork from is pristine; the fix, its shape, and its cost are yours to argue.

**Things I did NOT establish, and you must not assume:**

- Whether the same hole explains duplication *without* a resize (a genuine
  `t.prev`/terminal disagreement from `planScroll`, or the `screenSpare` recycle).
  The claim in the source that a mis-detected shift "costs bytes, never
  correctness" is **unverified across a width change** — test it.
- Whether `resize` also needs to invalidate `predBuf`, `keysOld/keysNew`,
  `screenSpare`, `rowCache`, `cacheW`, `prefix`. `t.prev = nil` disables
  `planScroll` for one frame (`len(prev) != h`), which may be *why* the plain
  diff's blank-row hole is the visible symptom. I did not audit the others.
- Whether a full repaint is the right fix or whether `resize` should re-emit
  `\x1b[2J` (flicker, which that comment was avoiding on purpose) or re-home the
  cursor before the shift. **That is a trade-off between two defensible
  renderings — if you end up choosing, hand it up as `BLOCKED:`, do not pick.**
- CHERUBINO's bug. I have **no capture** of it — but I did the static audit, and
  it points hard at the brief's second branch. See §8.3.

### 8.3 CHERUBINO: a located mechanism, not yet a capture

The brief tells you to distinguish (a) a row reaching the screen without
`clipToWidth` from (b) something writing outside the frame buffer. **Two pieces
of evidence, both mine, both point at (b).**

*Evidence 1 (measured).* Across 9 frames in the §8.1 sweep, at widths 100, 72,
120 and 64, `widest_row` equalled the viewport width **exactly** and never once
exceeded it. Invariant #1 is holding on every path I drove. That is not proof —
I never provoked an error — but it means (a) has no support yet.

*Evidence 2 (read, then confirmed by reading again).* Combine two facts:

1. **There is no scrollback inside the pager** (§3c, measured: `history_size=0`).
   Anything written to stdout/stderr while the alt screen is up lands **on the
   alt grid**, at the cursor — and the painter finishes every frame by writing
   `screen[t.h-1]`, the status row, so *the cursor is parked on the status row*.
2. **`livelogTurn.finishTurn` does not leave the pager.**
   `internal/cli/livelog_bridge.go:561`:

   ```go
   func (t *livelogTurn) finishTurn(reason string) {
       t.status.finishTurn(reason)
       t.finished = true
       if t.tr.active {
           t.tr.render()
           return          // <-- the pager stays up
       }
       …
   }
   ```

Now read the error path in `internal/cli/stream.go:155-170`. Its comment states
the intent plainly:

> *"Tear the live region (incl. an un-adopted thinking footer) down **FIRST**, so
> an error hint printed straight to the terminal lands on clean scrollback below
> it, not over the footer."*

```go
lt.finishTurn(d.Reason)
if isErr {
    …
    fmt.Fprint(os.Stderr, "\n"+providerSetupHint())   // no credential / resolve token
    …
    fmt.Fprintln(os.Stderr, "\n"+d.Reason)            // every other error
}
```

That reasoning is **sound for the inline view and false for the pager.** Inline,
`finishTurn` really does tear the live region down and scrollback really does
exist below it. In the pager, `finishTurn` returns early, there is no scrollback,
and the hint is written straight onto the alt grid at the cursor — i.e. **over
the footer, which is exactly what the comment was trying to prevent.** "Errors
where the text bleeds into the status bar," in the user's words.

Other writes that bypass the frame buffer, same hazard, worth checking in order:

| site | text | reachable with the pager up? |
|---|---|---|
| `stream.go:167` | `"\n" + providerSetupHint()` | **yes** — `finishTurn` returned early |
| `stream.go:169` | `"\n" + d.Reason` | **yes** — same path; the likeliest culprit |
| `stream.go:358` | `"\ninterrupting..."` | **yes** — written *before* any abandon/leave, so Ctrl-C during a running turn |
| `stream.go:368` | `"interrupted"` | probably; same block |
| `stream.go:346` | `"follow: figaro listen …"` | **no** — `abandon()` calls `leaveTranscript()` first (`livelog_bridge.go:495`) |
| `angelus_client.go:199,208` | build-mismatch warnings | only at startup, before the pager |

Note the leading `"\n"` on the first three: on an alt screen with the cursor on
the last row, a newline **scrolls the whole grid up one line**, so the damage is
not one row but a one-row shift of everything plus text on the status row. That
is a second-order symptom worth capturing.

**How to provoke it cheaply.** `stream.go:358` needs only a *running turn* and a
Ctrl-C — no error, no broken credential. `stream.go:169` needs a turn that ends
in an error while the pager is up; the cheapest honest way is a turn against a
deliberately bad model name or a revoked/blank credential **in the scratch config
only** (`FIGARO_CONFIG_DIR` is yours to corrupt — never the real one). Capture
with `pp_raw` (escapes kept) so you can see the SGR of the status row and prove
the text is not going through `footerRows`' `\x1b[2m … \x1b[0m`.

**This is a real product decision, so do not make it alone.** The fix could be
(i) leave the pager before printing, (ii) route the hint through the frame buffer
as a status/error row, or (iii) suppress it while the pager is up and surface it
in the status panel. Those are three visibly different behaviours. Pick the
evidence, then hand the choice up as `BLOCKED:` with the options — that is what
the brief's "When to yield" section is for.


---

## 9. If BASILIO and BARTOLO converge

One of you owns the fix on one branch; the other owns the **regression tests** —
a VT-harness case (deterministic, in `transcript_paint_test.go`, asserting the
screen after *every* frame per painter invariant #5, not just the last) plus a
tmux case extending `transcript_paint_tmux_test.go`. Agree who takes which
before either of you commits, and tell ALMAVIVA.

**Canary every test you write** (the rule that outranks all the others): revert
the fix, run the test, and **quote the failure** in `PROPOSAL.md`. An assertion
that has never failed is not evidence. The oracle in §5 makes this easy — with
the probe binary the diff is empty, with the shipped binary it is three rows.

---

*Ogni cosa misurata, niente indovinato.* — everything measured, nothing guessed.
