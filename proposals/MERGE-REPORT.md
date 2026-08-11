# MERGE-REPORT.md: `paint/merge`

**ALMAVIVA.** Three children, three branches, one merge. Everything here was
measured; where it was not, it says so.

**Do not merge to main.** The master coordinates that himself.

---

## The number

```
cd /home/gluck/dev/figaro-qua/paint-merge
go build ./...   OK
go vet  ./...    OK
go test ./...    exit 0    31 ok · 4 no-test-files · 0 FAIL
```

`FIGARO_TMUX_SMOKE` is unset, so CHERUBINO's smoke case is **opt-in and not
counted above**. On shipped code it FAILs by design (that is its whole point);
under a `leaveTranscript()` probe it PASSes. **He flags the opt-in gating as a
risk himself**, a test that only runs when someone remembers is a test that
stops guarding.

## What merged

| branch | HEAD | brought |
|---|---|---|
| `fix/resize-dup` | `eadecf8` | **the fix** (one conditional), the comment audit, the verbatim sentence in-source |
| `fix/gap-rows` | `9906169` | **the two canaried regression tests** + 312-frame falsification suite + `scripts/paint-gapcheck.sh` |
| `fix/status-bleed` | `008d215` | **the canaried smoke test** (no fix, a live `BLOCKED:`) + `smokeStore` hardening |

Base is `paint/base` (cookbook + harness). Merge HEAD `3ecf504`.

## The one conflict, and how I resolved it

**`PROPOSAL.md`, add/add, all three children.** They were each told to write it
at the worktree root, so all three collide at one path.

Resolved by parking each under `proposals/<NAME>-<topic>.md` and **taking both
sides in full**: no content dropped, no arbitration between them. I was told
that if a conflict forced a choice between two defensible resolutions, to take
the one that **keeps a canary legible**; that is this one, and I am recording
that I did.

## THE RED IS THE CANARY

Stated in those words, because it will otherwise look like a mistake:

**`fix/gap-rows` is RED ALONE, BY DESIGN.** Build and vet green; the only
failures are its three resize assertions, because the fix lives on BASILIO's
branch. That red *is* the canary. The two branches **together** are green, which
is the claim, and `paint/merge` carries it.

BARTOLO deliberately did **not** merge the fix into his branch so the canary
stays legible in history. BASILIO deliberately did **not** amend his fix commit
`90c4dac`: BARTOLO holds that sha as his canary reference, and rewriting it
would destroy the one ceremony neither of them can perform alone. **I signed off
on both.** Green-in-combination is the honest claim; making a branch *look* green
would have cost the evidence.

## The bug, in one paragraph

**`transcript.paint`, `internal/cli/transcript.go`.** *Diff vs nil is not a full
repaint.* The painter read its base row as `var old string; if r < len(base) {
old = base[r] }`, which makes **"no record of this row" indistinguishable from "a
record of an empty row"**: so every row whose new content is `""` compared equal
to a base that did not exist and was **skipped**. Blank rows are everywhere:
`entryLine` returns `""` for row 0 of every message separator. `resize` sets
`t.prev = nil` to mean *"the terminal reflowed under me, I know nothing"*, under a
comment claiming that bought a full repaint. It did not. The fix guards the skip
on having a record at all: `if r < len(base) && screen[r] == base[r]`.

**One root cause, two user reports.** A shrink makes tmux delete *k* rows from the
top, so terminal row *r* holds the old frame's row *r+k*: which the new frame
very often *also* renders at its own correct row. Measured: of 3 surviving ghosts,
1 was simultaneously on screen legitimately. "Lines are duplicated" and "gaps hold
text from some other line" are **the same pixels from two ends**.

## Unfinished: one line per child, plus mine

- **BASILIO: no growth-safe PTY oracle exists.** Height *growth* changes the row
  total (a taller viewport pulls history: `1028+ → 1212+`), so the jog-and-diff
  oracle lands on a different viewport and correctly **refuses to judge**. He
  settled growth in the VT harness instead and proved all three directions fail on
  pristine and pass on his branch. The real-terminal gap is **flagged, not
  papered over**.
- **BASILIO: `gapRow` has a latent invariant #1 violation, and it is NOT the
  user's bug.** Say that second part first. `go-runewidth` takes
  `EastAsianWidth` from the environment at init; under `LANG=ja_JP.UTF-8` the
  box-drawing dash, vertical bar, selection cue, middle dot and ellipsis are all
  **width 2**. `ruleLine` survives because it ends in `clipToWidth`; **`gapRow`
  does not**: its non-degenerate path is unclipped arithmetic. Measured with
  `gapRow`'s exact code at `w=100`: **100 columns under `LANG=C`, 180 under
  `ja_JP.UTF-8`**: 80 over the viewport, which wraps, and a wrapped row desyncs
  the painter. **The master's locale is `en_US.UTF-8`, so this is correct on his
  machine and explains no symptom he has reported.** Nobody should chase it as the
  status-bar bleed. Two cheap follow-ups: restore `clipToWidth` on `gapRow`, and
  correct the `commonRowPrefix` comment which claims runewidth agreement is
  "guaranteed for ASCII and box drawing": it is **false**.
- **BARTOLO: `naivePaint` still carries the bug.** The reference oracle in
  `TestTranscriptPaint_MatchesNaiveRepaint` is *also* a diff painter that skips
  empty rows against a short prev, so it agreed perfectly with the bug it existed
  to catch. He wrote his own reference as an **unconditional repaint**; the old
  oracle is left in place and unfixed. Drafted as a proposed twelfth trap in
  `skills-patch-trap12.md`: **a patch file, not an edit to the master's skills.**
- **CHERUBINO: no fix, deliberately: a live `BLOCKED:`.** Also `stream.go:167`
  (`providerSetupHint`) is **presumed by inspection and NOT captured**, and he
  says so rather than implying he photographed it. `:169` and `:358` are captured;
  `:346` is the verified-safe negative control.
- **ALMAVIVA: my oracle has a documented blind spot.** BARTOLO showed
  jog-and-diff **compares the painter against itself**, so damage the repair
  gesture does not repair leaves suspect and truth *equally wrong*, the diff
  empty, and the verdict **CLEAN**. That is exactly where a second root cause
  would hide, and exactly what the user's *"typically* fixed upon return" left
  room for. It is **not retired**, a documented blind spot beats a silent
  replacement, and the limitation is attached to it in `PAINT-REPRO.md` §5.
  His 312-frame VT suite is the instrument that closes the hole.

## OPEN AND UNOWNED: restated so it cannot evaporate

Nobody is working on these. They are recorded because BASILIO restated them
rather than let them dissolve at the end of a long night, which is the correct
instinct.

| item | status |
|---|---|
| **Make `pp_fixture` fill a pane.** More nodes rather than one, or expand the node, or raise the 200-line retention. **The acceptance test is "the footer shows a RANGE"**, not "the script exits 0", a fixture with no range cannot fail. Whether `Enter`/`C-o` lifts the `… last 10 of 200` collapse is **UNMEASURED.** | open |
| **Restore `clipToWidth` on `gapRow`**, and correct the `commonRowPrefix` comment claiming runewidth agreement is "guaranteed for ASCII and box drawing": **false**. Latent, locale-dependent, **NOT the master's bug** (his locale is `en_US.UTF-8`). | open |
| **Add the unstated precondition to `planScroll`'s** "a mis-detected shift costs bytes, never correctness": it holds **only while `t.prev` equals the terminal**, which was silently false after every resize. | open |
| **`naivePaint` itself is unfixed.** BARTOLO wrote his own reference as an unconditional repaint and left the mirror in the tree. | open |
| **A growth-safe PTY oracle does not exist.** Height growth changes the row total, so jog-and-diff correctly refuses to judge it; growth is covered only in the VT harness. | open |

**Both sources of a range-carrying transcript are currently closed**, which is why
the real-pty repro path is labelled *the shape, not a recipe*: a real aria needs
`PP_SEED_REAL_STORE_I_ACCEPT_THE_PRIVACY_COST` (which should stay closed), and the
synthetic fixture yields no range so `pp_require_range` correctly refuses it. The
deterministic `go test` path is the one that **runs today**: no store, no daemon,
no terminal, no tokens.

## The decision the master must make

**`BLOCKED: CHERUBINO`**: how should an error render when it arrives while the
transcript pager is up? Photographed as **(b)**, a writer bypassing the frame
buffer, on two independent grounds: the bleeding row carries **no SGR at all**
while every genuine footer row is `\e[2m…\e[0m`, and the footer sits two rows
above where the painter put it (`Fprintln` = leading + trailing newline; on an alt
screen with the cursor on the last row each newline scrolls the grid). **A
DUPLICATED STATUS BAR IS THEREFORE THE SAME BUG**: it is on the shipped-bug list
in the tmux-testing skill, so anyone chasing it from that end is chasing this,
which makes the fix worth more than it looks.

| option | cost |
|---|---|
| **(i)** leave the pager, then print | probed and working, test passes; costs an unrequested view change + a `flushTail` dump |
| **(ii)** route through the frame buffer as a styled status/error row | least disruptive, keeps the reader's place, consistent with invariant #1. **Costed to the field by CHERUBINO and materially smaller than it sounds: ONE new field** (`reason string` on `sessionStatus`, set under the existing mutex), **one new row** in `panelLines`, and a `clipToWidth` summary via the existing `jumpNote` mechanism. No new machinery, no new invariant, no new lock. |
| **(iii)** suppress while the pager is up, surface in the `!` panel | cheapest, correct by construction; **an error the user never opens a panel to read is an error he may never see**: which contradicts the stated reason these writes exist |

**Precedents that make (ii) a reuse rather than an invention**, all verified by
CHERUBINO in the source, not assumed:

- `transcript_jump.go:186` already does `t.jumpNote = err.Error()`. **An error
  string on the status row is existing shipped behaviour**, cleared on the next
  key at `transcript.go:1671`.
- `showQueuedAuto` is a better precedent than "three panels exist": it opens a
  panel because **state** changed rather than a key, and it already carries the
  policy a reviewer would demand: *a panel the user opened by hand is never
  auto-closed*. The hard question is pre-answered.
- `statusPanelLines` already clips to `t.h-4`, so a long hint is **bounded by
  construction** and cannot overrun the frame.
- `sessionStatus` already has `turnStatusError` and `finishTurn(reason)`.
- **The one real gap:** `finishTurn` *classifies* the reason and **throws it
  away**: it lowercases it, switches on it to set `s.turn`, and never stores it
  (`internal/cli/session_status.go`). That single omission is the whole cost.

**The multi-line sub-decision does not need to reach the master as a choice.**
CHERUBINO argued it out: truncating `providerSetupHint` to one row keeps the
*diagnosis* and discards **every actionable line**: the user learns he has no
credential and not one way to fix it, turning a recoverable error into a dead end,
which is worse than the bleed it replaces. Truncation *is* right for a single-line
reason like `error: anthropicsdk 401: …`. So: **summary row always, panel only when
the text does not fit.** Complementary, not alternatives.

**The corruption outlives the error.** CHERUBINO's addition, and it is the most
important thing for pricing: once `t.prev` describes a terminal that no longer
exists, **every later frame is diffed against a lie.** This is not "an error
smudges the footer once": it is "an error permanently desynchronises the painter
until something forces those rows to differ". That is the **same broken invariant**
BASILIO and BARTOLO reach by a different route: their fix and this one are two
doors into one class of bug.

**Recommended (ROSINA concurs, and it is CHERUBINO's refinement of her prior):
(ii) for the error paths, (i) for Ctrl-C.** Ctrl-C is *already* an exit gesture so
leaving the pager costs nothing there; an error arriving mid-read is exactly when
the reader wants his place kept. For (ii)'s sub-decision, the recommendation is
**auto-open the `!` panel** for a multi-line hint and put a one-line summary on the
status row: the mechanisms already exist (`jumpNote` owns the status row today;
three panels already open and close), so this is no new machinery.

**Nothing has been implemented.** CHERUBINO has the probe for (i) in hand and can
land whichever is chosen in one commit.

## Also for the master, separately

- **Credential rotation.** Four copies of `providers/anthropic.toml` were
  world-readable in a `1777` `/var/tmp` for roughly **01:21–01:47** on a box
  running k3s. Mode `700` does not stop root in a container with `/var/tmp`
  mounted. One human account, so real-world risk is low. **Closed at the source**
  (credentials are now never copied; config is shared by reference, so the failure
  mode is deleted rather than shrunk), every copy cleared, and measured: **zero
  `*.age`, zero `hush/`, zero credential files** across every tree under my
  control. **Whether to rotate is yours; I did not decide it and did not read the
  file.**
- **`008d215` (`smokeStore` hardening) is deliberately droppable.** Same latent
  shape in *shipped test code*, measured **CONTAINED: not an active leak**
  (`smokeStore` creates its config subdir `0700` before copying, and
  `t.TempDir()` is removed at test end rather than surviving reboot). A one-commit
  revert is the whole point.

## Warning to anyone measuring resizes next

From SUSANNA: **a table's row count at a given width changes after
`feat/table-wrap`** (cells wrap instead of truncating), so a resize across a table
produces a different row-count *delta* than it used to. No blank rows either way -
she pinned that with a canaried test: but a fixture containing a table will shrink
and grow by a different magnitude pre- and post-merge, and someone will chase a
phantom. **Keep paint fixtures table-free**, or rebase onto her branch before
measuring. `pp_fixture` is table-free by construction (bare integers).

## What I got wrong, since a report without this is marketing

1. My first draft blamed a **vertical terminal shift**. A width-only resize
   contaminates too, so the shift is an amplifier, not the cause. The sweep
   corrected me.
2. I told BERTA I had removed a socket **I had never removed**. `pp_verify_clean`
   exists because of that.
3. I reported `/var/tmp/paint-rosina` as exposed having **measured only that the
   path existed**, never its mode. Retracted. The existence was true when
   written; the exposure was inferred.
4. I fixed BERTA's `pgrep -x tmux` trap and **committed the identical bug one
   function below**, matching `comm = figaro` against daemons named
   `figaro-after`. The name is never reliable in either direction: BERTA later
   found five live daemons named `fig`, one running from a **binary deleted from
   disk**.
5. I declared the credential leak closed having fixed `/var/tmp` and **left
   `/tmp/paint-*` at 755 with 99 world-readable captures of the master's
   conversation.** BASILIO caught it.

*Ogni cosa misurata, niente indovinato.*
