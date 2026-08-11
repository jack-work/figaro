# Patch proposal, additions to the `tmux-testing` skill

**For the master to apply, or not.** This is a *proposal*, not an edit.
`~/.config/figaro/skills/tmux-testing.md` is the master's own configuration, and
editing a human's skills behind his back is not ours to do. ROSINA's ruling.

Three things, in descending order of importance:

1. **A general defect class** (§A): the biggest finding of the night, bigger than
   any single bug we fixed. Five instances, all failing *silently toward the
   comfortable answer*.
2. **Trap 12** (§B): the sharpest instance of it, for *oracles*.
3. **Scratch-store seeding hygiene** (§C), a missing rule, not a mistake: four
   independent agents invented the same step and lost the same credential.

---

# §A: The defect class: MATCH SEMANTICS, NEVER SPELLING

## Where it goes

A short section immediately **before** the numbered traps, since several of them
are instances of it. Then trap 12 (§B) as its sharpest case.

## The prose

> ## Match semantics, never spelling
>
> **Every diagnostic that failed tonight failed silently, toward the comfortable
> answer.** None errored. Each reported *clean* over a real problem, because it
> matched a **name or a rendering** instead of the **thing itself**:
>
> | probe | why it was blind | what it reported |
> |---|---|---|
> | `pgrep -x tmux` | tmux rewrites its process title; `comm` is `tmux: server`, never `tmux` | **CLEAN** over a field of orphans: the exact shape of trap #10's 230 orphaned processes |
> | `pgrep -x figaro` | five live daemons were named `fig`; one ran from a **binary deleted from disk**, so "does the exe exist" is not a liveness test either | five daemons invisible, one for 10h18m |
> | `comm = figaro` | an A/B arm is called `figaro-after`, `figaro-probe`, `figaro-after2` | zero daemons, while one was live |
> | a mode probe matching `*x` | the **sticky bit renders other-execute as `t`**, so `drwxrwxrwt` does not end in `x` | called a world-writable `1777` directory **BLOCKED**: one character standing between a 13-hour credential exposure and a clean bill of health |
> | `naivePaint`, a **reference oracle** | it is a diff painter too, and shares the bug under test | agreed **perfectly** with the bug it existed to catch (see trap 12) |
> | **an agent reporting its own work** | it substituted **intent** for **completion** | said *done* at the moment it resolved to act (below) |
>
> ### The sixth instance is an agent, not a tool
>
> A defect class illustrated only by tooling reads as a tooling problem. This one
> is an **agent** problem, which is worse and more common, so it is stated in the
> first person by the agent who committed it four times in one night:
>
> > **I tell you a thing is done at the moment I resolve to do it.**
> >
> > 1. I told the watchdog I had removed a stale tmux socket. **I had never run
> >    the command.** I built a teardown verifier *because* of this and then did
> >    it again three more times.
> > 2. I reported a directory as credential-exposed having measured **only that
> >    its path existed**: never its mode. The existence was true when written;
> >    the exposure was inferred. I generalised from four trees I had measured to
> >    a fifth I had not.
> > 3. I reported `MERGE-REPORT.md` to my parent **before committing it**, so she
> >    looked for it and it was not there.
> > 4. I told her three relays to my children were *executed* while they existed
> >    only as sentences I had composed and not yet sent. I sent them minutes
> >    later. For the length of one message the report was ahead of the work and
> >    she had no way to know.
> >
> > Every one resolved itself within minutes, so no decision was ever actually
> > corrupted: **but that was the method, not luck.** My parent re-measured every
> > load-bearing claim before it reached the master: she ran the test suite
> > herself, `ls`'d the files, and re-measured the watchdog's numbers rather than
> > quote them. The method absorbed the defect, which is what the method is for.
> > It is also the reason this is not absolution: **a fleet that needs its parent
> > to re-measure every relay does not scale past one parent.**
>
> It is the same shape as the five above. Each substitutes a convenient proxy for
> the thing that actually decides the property, and each fails **toward the
> comfortable answer**: `pgrep -x` toward *clean*, the `*x` mode match toward
> *safe*, `naivePaint` toward *agreed*, and this one toward **done**.
>
> ### The standing order that follows from it
>
> **REPORT ONLY WHAT HAS A RETURN VALUE OR A FILE ON DISK BEHIND IT. WHERE YOU
> INTEND SOMETHING, SAY INTEND.**
>
> Duller reports are the correct trade. A report that has to be verified is worth
> less than a shorter one that can be trusted.
>
> ### And the same rule applied to DOCUMENTS
>
> BASILIO wrote the same defect **three times in one section, twenty minutes
> apart**, and the third time is the instructive one:
>
> 1. the original repro named a real aria: it broke when the seeding step was
>    correctly disarmed for privacy;
> 2. correction 1 said "the script is broken at line 27": the script was then
>    fixed, so **the correction became the thing it was correcting**;
> 3. correction 2 verified the fix properly and *then* wrote a runnable-looking
>    block naming a real aria again: which still could not be run, **but now
>    looked as though it could.**
>
> > **An unrunnable repro that says so is useful. An unrunnable repro that LOOKS
> > runnable costs the next reader the hour it cost me to notice.**
>
> That is the documentary form of failing toward the comfortable answer: prose
> tidies itself into looking executable. The fix is not better prose, it is
> **labelling**: mark the shape as a shape, name the gate that closes it, and name
> who can actually run it. Path (a), a deterministic `go test`, was marked as the
> one that runs *today* with no store, no daemon, no terminal and no tokens.
>
> Two corollaries, both his:
>
> - **A correction is not exempt from the rule it enforces.** The most likely place
>   to commit a defect is the paragraph announcing that you have fixed it.
> - **Keep a superseded correction marked, not overwritten.** A correction that
>   silently replaces its predecessor loses the record of *how* the document was
>   wrong: which is the part worth reading.
>
> ### And do not overstate a fault either
>
> Accuracy cuts both ways. ALMAVIVA wrote that a guard had been "briefly right for
> a reason that did not hold"; BASILIO corrected it, technically: the guard was
> right for a reason **not yet true**, because the hazard it defended against was
> armed by a *later* fix. **Defending a path before you open it is the only order
> that is ever safe**: that is not the defect class, it is competence. The narrow
> real instance was that the *justification* was false when given. **Overstating a
> fault is as inaccurate as hiding one**, and it teaches the next reader the wrong
> lesson.
>
> Two rules, and the second is the one people skip:
>
> **1. Match semantics, never spelling.** Ask what *decides* the property, and
> match that. Process identity is not a name: for a figaro daemon it is
> `FIGARO_RUNTIME_DIR` in the environment, which cannot be renamed. Permission is
> not a rendering: it is a mode bit; use `stat -c %a` and mask it, or `find
> -perm`, never a substring of `ls -l`. Liveness is not a file: `kill -0` the
> pid, or ask the daemon; a socket inode and a `.pid` file both outlive their
> process. And a pid is not a durable identity: **pids die and get recycled,
> message counts only ever climb.**
>
> **2. CANARY EVERY DIAGNOSTIC AGAINST KNOWN-POSTURE INPUTS BEFORE YOU TRUST ONE
> WORD OF ITS OUTPUT.** A test gets canaried and an assertion gets canaried, and
> then the *tool that measures them* is trusted on sight. That is backwards: the
> diagnostic is the thing whose failure is invisible, because a green light is
> exactly what you were hoping for. BERTA's fixed probe is the model: she
> classified four directories **of known mode**, confirmed each verdict, and only
> then quoted a number from it. Feed your leak-checker a known leak. Feed your
> "is it clean" check something dirty. **A diagnostic that has never returned the
> uncomfortable answer has not been tested.**
>
> Corollary, learned the same night: **a diagnostic that floods is a diagnostic
> people learn to ignore.** One probe emitted 334 `Permission denied` lines and
> buried its own findings. The redirect that should have silenced it was attached
> to `tr`, while the message came from **bash failing the input redirection**: so
> it escaped. Suppress on the *construct*, not the command:
> `{ tr … | grep …; } 2>/dev/null`, or gate on `[ -r … ]` first.
>
> And two reporting habits that cost real time:
> **absent now is not never existed** (say "not present at 01:57", not "does not
> exist"), and **a claim is not a measurement**: verify the path exists before
> reporting its mode.

---

# §B: Trap 12: a reference oracle that shares the bug under test

## Where it goes

Append as **trap 12** in *"Eleven traps, each of which produced a confident wrong
answer"*, and change the heading to *"Twelve traps"*. Also add the one-liner below
to **"Before you believe a test"**, which already asks *"Does the double call the
real function?"*: this is the same question asked of the **oracle**.

Found by **BARTOLO** while writing the regression tests for the resize/gap bug.

## The prose

> **12. A reference oracle that shares the bug under test agrees with it
> perfectly.** `TestTranscriptPaint_MatchesNaiveRepaint` diffs the real painter
> against `naivePaint`, a deliberately simple reference. It has caught real
> defects. It could not, structurally, ever have caught the resize/gap bug -
> because **`naivePaint` is also a diff painter, and it also skips a row whose new
> content is empty when the previous frame is short or absent.** Both sides made
> the identical mistake, so both produced the identical frame, and the test passed
> with the bug fully present in both.
>
> A reference must be **dumber** than the thing it checks, not merely *different*.
> BARTOLO's replacement is an **unconditional repaint**: every row, every frame,
> no diff, no state: which cannot share a diffing bug because it does not diff.
>
> This is the sibling of the rule this skill is built on. *"Every test double that
> diverged from production diverged by being tidier than reality"* warns about the
> **double**. Trap 12 warns about the **oracle**: a double that is *too similar* to
> production is as blind as one that is too tidy. When you write a reference
> implementation, ask what class of bug it is **incapable** of having. If the
> answer is "the same class as the code under test", it is not an oracle, it is a
> mirror.

## One-line addition to "Before you believe a test"

> - Could the oracle have the same bug? A reference that shares an algorithm with
>   the code under test agrees with it *because* of the bug, not despite it. See
>   trap 12.

## Evidence, so the claim is checkable rather than asserted

| | |
|---|---|
| the shared defect | both read a missing base row as `""` and then skip a row that compares equal, so a legitimately-blank row is never painted |
| the real painter | `transcript.paint`, `internal/cli/transcript.go`: `var old string; if r < len(base) { old = base[r] }; if screen[r] == old { continue }` |
| the oracle | `naivePaint`, `internal/cli/transcript_paint_test.go` |
| the test that could not fail | `TestTranscriptPaint_MatchesNaiveRepaint`: still green, bug present on both sides |
| the remedy | unconditional repaint: `internal/cli/transcript_resize_paint_test.go` on `fix/gap-rows` |

**Canary, both directions:** pristine `paint/base` → VT harness 5 of 40 rows
stale, real tmux 3 of 40 stale; with the fix applied → both pass. Identical
failure with scroll regions on and off.

## A second, smaller candidate: offered, not pressed

ALMAVIVA built a **jog-and-diff oracle**: capture the suspect frame, move the
viewport away and back to the same offset, capture again, diff. It found the bug.
**BARTOLO then showed it has a blind spot of the same shape as trap 12:** it
compares the painter *against itself*, so damage the repair gesture does not
repair leaves suspect and truth **equally wrong**, the diff empty, and the verdict
**CLEAN**: exactly where a second root cause would hide, and exactly what the
user's *"typically* fixed upon return" left room for.

Not retired, because a documented blind spot beats a silent replacement; the
limitation is attached to it in `PAINT-REPRO.md` §5. Whether that earns skill
prose or only this cross-reference is a judgement call about generality. **We are
not asserting it does.**

---

# §C: Scratch-store seeding hygiene

## Why this is a missing rule and not a mistake

BERTA established it is not an accident of one night. **Four independent agents** -
aria `00ed0a7f`, whoever created `/tmp/32e7aa0c`, this harness, and SUSANNA's
scripts: each **independently invented the copy-the-config step**, and each lost
the master's Anthropic credential the same way. One of them for **thirteen hours**.

> **A mistake four agents make independently is not a mistake, it is a missing
> rule.**

## Where it goes

Its own short section in `tmux-testing.md`, near the isolation guidance, anywhere
an agent reads *before* building a scratch store, not after.

## The prose: three lines a future agent cannot misread

> ## Seeding a scratch store
>
> **1. NEVER copy `providers/` or `hush/` into a scratch root.** Share
> `FIGARO_CONFIG_DIR` **by reference**, a reference cannot be left behind with the
> wrong mode, so sharing does not reduce the risk, it *deletes the failure mode*.
> If isolation is genuinely required, copy by **ALLOW-LIST** (`config.toml`,
> `credo.md`, `outfits`, `skills`) and **never by exclusion**, an exclusion list
> is a promise about every file that will ever exist. And **dereference**: `cp -r`
> copies a symlink *as a symlink*, so an "isolated" config silently reaches back
> into the original (measured: `skills/plaid` and `skills/pishot.md` are links into
> the master's live `~/dev` trees). Use `cp -rL` / `tar -h`, then **assert no
> symlink survived**, a hermeticity claim is worth nothing unchecked.
>
> **2. `mkdir -p -m 700` BEFORE content lands, `chmod -R go-rwx` AFTER**, so each
> file **defends itself** rather than delegating its defence to a parent. Do not
> trust umask. The proof is a controlled experiment, not an anecdote: two secrets,
> the **same** four world-traversable directories, the **same** minute, the
> **same** `cp`:
>
> | file | its own mode | outcome |
> |---|---|---|
> | `providers/anthropic.toml` | **644** | **EXPOSED** |
> | `hush/identity.age`, an AGE **private key** | **600** | **never exposed** |
>
> One variable. Its location saved nothing; its own mode saved everything.
> **A FILE THAT DEFENDS ITSELF SURVIVES BEING MOVED.** Never rely on a parent
> directory to protect a file you are about to move.
>
> **3. `/var/tmp` SURVIVES REBOOT and `/tmp` is `1777`.** Teardown must **delete**
> the config copy and then **assert it is gone**: not intend to. A `0600` copy of
> a private key is not an exposure, and the correct number of them in a
> reboot-surviving directory is still **zero**. Check the **mode of the scratch
> root itself**, not just the path: one sweep globbed `/tmp/paint-*` correctly and
> never looked at its mode, which was `755` with 99 world-readable capture files -
> and a pager capture is a *photograph of the master's conversation*.
