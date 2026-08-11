# PROPOSAL: BASILIO · resize duplication

Branch **`fix/resize-dup`** (forked from `paint/base`). One-line fix, measured
rather than argued. `go build ./... && go vet ./... && go test ./...` green.

Owner split, agreed with BARTOLO in writing before either of us committed: **I
own the fix, BARTOLO owns the regression tests.** The canary is therefore a joint
act: see [Canary](#canary), which is the one deliverable neither of us can fake
alone.

---

## 1. The bug

The user, twice, from two ends:

> "When the terminal resizes, very often lines in transcript mode are duplicated."

> "The gaps in between nodes are populated with text that shouldn't be there from
> some other line. It can only be cleared by moving the viewport such that the
> corrupt region is no longer visible, and then moving back to that area. It will
> typically be fixed upon return."

**Those are the same pixels.** §4 proves it rather than asserting it.

## 2. Repro

Runnable by another aria, no provider, no tokens, ~4 s to a loaded pager:

```sh
cd /home/gluck/dev/figaro-qua/resize-dup
./scripts/paint-jogdiff.sh basilio 8566c903                      # this branch: CLEAN
./scripts/paint-jogdiff.sh basilio 8566c903 <a-pristine-binary>  # paint/base: CONTAMINATED
```

By hand (`PAINT-REPRO.md` §4–5): stand up a 100×40 pane, `figaro listen
8566c903`, `^T`, `gg` then three `d`, resize to **100×72: width only**, capture,
jog the viewport six half-pages away and six back to the *same* offset, capture,
diff. Any difference at the same footer range is the bug.

Width-only is the better trigger: it needs **no** vertical row movement, which
falsifies any explanation resting on the terminal's own grid shift.

## 3. Root cause

**`transcript.paint`, `internal/cli/transcript.go`.** The painter read the base
row it diffs against as

```go
var old string
if r < len(base) { old = base[r] }
if screen[r] == old { continue }
```

which makes *"no record of this row"* indistinguishable from *"a record of an
empty row"*. `transcript.resize` sets `t.prev = nil` to mean "the terminal
reflowed under me, I know nothing about the screen", and its comment claimed that
bought a `full repaint (diff vs nil)`. It did not. It repainted every **non-blank**
row and silently skipped every blank one, because a blank row's new content is
`""` and `""` compares equal to a base that does not exist. Blank rows are
everywhere: `entryLine` (`transcript_index.go`) returns `""` for row 0 of every
message separator. So after a resize each separator's blank row kept whatever the
terminal had left in it, and stayed wrong until the viewport moved far enough to
make that row *differ* from `t.prev`: "typically fixed upon return".

## 4. Why "duplicated" and "gap contamination" are one bug

Proven, and one of my own hypotheses died on the way.

**Falsified.** I predicted `planScroll` would shift the terminal's stale rows with
DECSTBM+SU/SD and *multiply* the ghosts. **It does not.** Ghost count is dead
constant at 4 across 12 successive one-row scrolls: stale rows **relocate, they
never duplicate**. The source's claim that a mis-detected shift "costs bytes,
never correctness" survives: but it has an **unstated precondition**: it is only
true while `t.prev` actually equals the terminal. Worth a comment someday; I did
not add one, as it is not this bug.

**Confirmed.** A shrink makes tmux delete `k` rows from the **top** (measured,
`PAINT-REPRO.md` §6), so terminal row `r` holds the *old* frame's row `r+k`. The
new frame covers mostly the same viewport, so that old line is very often **also
rendered in the new frame at its own correct row**. Measured on synthetic
content: of 3 surviving ghosts, **1 was a line simultaneously on screen
legitimately**: the user sees it twice. "Lines are duplicated" and "gaps hold
text from another line" are one event described from two ends.

## 5. The fix

One guard: **a row with no recorded base is unknown, not blank.**

```go
if r < len(base) && screen[r] == base[r] {
    continue
}
```

Also fixes the short-base case (a screen *taller* than the record), which is the
same hazard from the other side and which nobody had noticed. Costs nothing on
the hot path: when `base` is `prev` or `predBuf` it is always `len(screen)`.
I also corrected the two comments that stated an intent the code did not deliver.

### Why not the alternatives, and why this is **not** a `BLOCKED:`

My brief told me to escalate if choosing here was a visible behaviour trade-off.
**I measured, and it isn't**, so escalating would have been noise:

| candidate | correctness | cost | verdict |
|---|---|---|---|
| **guard the skip** (taken) | fixes every caller that nils `prev`, plus short-base | **+44 bytes/resize (+1.2%)** | dominates |
| `full := base == nil` (ALMAVIVA's probe) | fixes nil-base only | same bytes | strictly weaker, no cheaper |
| `t.prefix += "\x1b[2J"` | fixes resize only; leaves the painter footgun loaded | **visible flicker** | worse on both axes |

The measurement that removes the trade-off: the post-resize frame is **3730 bytes
before, 3774 after**. It is that cheap *structurally*: every non-blank row
already differed from a nil base and was already being retransmitted, so the only
rows added are the blank ones, at `CUP`+`EL` each. `\x1b[2J` would also have
fixed it and would flicker, which the original comment was right to avoid. There
was never a decision here, only a number somebody had to take.

**No user-visible behaviour change** except that the screen is now correct.

## 6. Evidence

**Real pty A/B.** Same script, same aria, fresh pane *and* fresh daemon per arm,
md5 printed so the arms provably differ (trap #11: two arms that produce
identical output are more often one binary than one bug):

| arm | md5 | result |
|---|---|---|
| before: pristine `paint/base` | `0e04173e36e9` | 5 clean, **3 CONTAMINATED**, 1 skipped |
| after: `fix/resize-dup` HEAD | `c1330222cc02` | **8 clean, 0 contaminated**, 1 skipped |

Contaminated cases: `width-72` (5 rows), `width-120` (3 rows), `both-64x20` (1).
Full log: `/var/tmp/paint-basilio/ab.log`; captures under
`/tmp/paint-basilio/jogdiff/`.

**Height growth, which the pty oracle refuses to judge.** A taller viewport pulls
history (total `1028+` → `1212+`) so the jog lands on a different viewport and the
oracle correctly prints `SKIP` rather than a tidy meaningless number. I settled it
deterministically in the VT harness instead. Pristine fails all three directions;
this branch passes all three:

```
grow  40->56 : 7 blank rows, 5 disagree      (pristine)  -> 0 disagree (fixed)
grow  24->40 : 5 blank rows, 3 disagree      (pristine)  -> 0 disagree (fixed)
shrink 40->24: fails                          (pristine)  -> passes    (fixed)
```

## 7. The invalidation audit ALMAVIVA left open

Does `resize` need to invalidate anything besides `prev`? **No. Verified by
reading each one:**

| field | why it is already safe |
|---|---|
| `rowCache`, `cacheW` | `buildIndex` clears the cache when `t.cacheW != t.w` (`transcript_index.go:132`), and `resize` calls `buildIndex`. Already correct. |
| `predBuf` | `planScroll` regrows it to `h` and `copy`s from `prev` before every use; never read stale. And it cannot run at all on the resize frame: it requires `len(prev) == h`, and `prev` is nil. |
| `keysOld`, `keysNew` | regrown to `h` and fully overwritten on every `planScroll` call. |
| `screenSpare` | `nextScreen` `clear()`s it before handing it out (or allocates). |
| `prefix` | only ever set by `enter()`, and consumed by the next paint. `resize` deliberately does **not** add `\x1b[2J`: see §5. |

Note the interaction ALMAVIVA suspected is real but benign: `prev = nil` disables
`planScroll` for exactly one frame, so the resize frame is always a plain full
diff and the scroll path re-arms next frame against a base that is now true.

## 8. Canary

**I did not write the regression tests: BARTOLO owns them, by agreement.** So
the canary is deliberately split, which is what makes it evidence: BARTOLO lands
his tests against **pristine `paint/base`** and quotes the failure; he then
applies my patch and quotes the pass. Neither of us can produce both halves
alone.

What I can attest, from my own scratch probes (run, then deleted: they are not
tests, they are measurements):

```
BEFORE (pristine): frame had 6 legitimately-blank rows; 4 row(s) disagree with t.prev   FAIL
AFTER  (this fix): frame had 6 legitimately-blank rows; 0 row(s) disagree with t.prev   PASS
```

The assertion has genuinely failed on unfixed code, in both the VT harness and a
real pty. It is not an assertion that has never failed.

## 9. Risk

**Low.** One conditional in one function; strictly *more* painting, never less,
so it cannot erase content that was previously drawn.

- Byte cost on the hot path: **zero**. `prev` and `predBuf` are always
  `len(screen)`, so `r < len(base)` is true on every steady-state frame and the
  compare is unchanged.
- Byte cost on resize: **+1.2%, measured**.
- Flicker: none: no clear is emitted, and the whole frame stays inside the
  existing `\x1b[?2026h` synchronized update.
- The existing paint tests (including the byte-thrift assertions
  `TestTranscriptPaint_UsesScrollRegion`, `TestPaintReusesBuffers*`, and the tmux
  replay) pass unchanged.

## 10. What was NOT done

- **Regression tests.** BARTOLO's, by agreement. My probes were deleted.
- **The `planScroll` precondition comment.** "Costs bytes, never correctness" is
  true but silently assumes `prev` equals the terminal. I left the comment alone
  rather than widen this diff; worth a follow-up.
- **A real-pty measurement of height growth.** Settled in the VT harness only,
  because the pty oracle honestly refuses. Building a growth-safe pty oracle
  (re-anchor by `/`-search rather than by jog) is unfinished work.
- **Falsification of the convergence claim beyond resize.** BARTOLO's lane: a
  live streaming turn, `/`-search + `n`/`N`, a history page landing mid-view, the
  `!`/`Q` panels, `gg`/`G`, the wheel. My sweep covered scroll, `C-n`, `Enter`,
  `C-o`, all clean, one aria, one geometry. **If he finds contamination with no
  resize, that is a second bug and this fix does not claim it.**
- **`enter()` and `leave()`**, which also nil `prev`, are now covered by the same
  guard, but I did not go looking for bugs there. `enter()` was never broken -
  it pairs its nil with `\x1b[2J`.

## 11. Decisions for the user

**None.** I looked for one and measured it away: see §5. The only judgement call
was fix shape, and the cheapest correct option is also the least visible, so
there is nothing to trade.

One thing worth a human's *opinion* rather than a decision: the pager suppresses
`\x1b[2J` on resize to avoid flicker and now genuinely does not need it. If the
user ever reports residual flicker or tearing on resize over a slow link, the
frame is a candidate for a `\x1b[2J` + full paint after all: but on this
evidence that would be a regression, not a fix.

---

*Misurato, non indovinato.*
