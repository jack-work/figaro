# Standalone forms, binding, and roles: the brief

Worktree `figaro-qua/forms`, branch `forms`, off `400bec6` (v0.23.0).
Written from Gluck's spec 2026-08-10. NOT started: see Part 0.

---

## Part 0: Why this is a brief and not a commit

The aria that took this spec was near the end of its context. Starting a change
this size there produces a half-migrated branch and a handoff written by someone
who can no longer explain it. The spec below is the valuable thing; it is
recorded while it is still exact.

Order of work: Part 2, then 3, then 4. Part 5 is explicitly OUT.

---

## Part 1: What exists to build on (v0.23.0)

- `store.Form`: the state object: one writer, lock-free published snapshot,
  `OpenForm(FormLog)`, `MemFormLog`. **Already needs no aria.**
- `store.ForkWith(parent, atMainLT, patch) (child, version, err)`: one birth
  verb; parent may be `""` (null root), a stump, or an aria. Patch required.
- `rpc.FormDelta` + `form.delta`: committed patches broadcast on the same
  fanout as aria frames; `Form.OnCommit` is the hook.
- `internal/form`: the tree and patch algebra, shared by client and server.
  `cli.formMirror` is a working client-side replica with resync.
- `fig form listen <id>`, alt-screen JSON tree over that mirror.
- `internal/actor`: the single-writer runtime, two users today.

The gap: every node figaro mints has BOTH an IR channel and a form channel, and
`fig ls` shows anything that is a conversation trunk.

---

## Part 2: Standalone forms

A form with no figaro bound. **No stump semantics**: every form forks the null
form and duplicates its state.

- `fig form new [-O <dressing>]`: `ForkWith("", 0, patch)`, writing the form
  channel and NO IR. Decide: does it write a main record at all? The cursor
  stamp exists to key patches to a turn; a form with no turns may not need one,
  but `writeBirth` currently writes one. Simplest honest answer: keep the birth
  record (it is what makes the patch renderable if a figaro is ever bound), and
  let "no IR beyond the birth record" be the definition of unbound.
- `fig form ls`: forms only. `fig ls` must NOT show them.
  **The discriminator is the open question.** Candidates: (a) IR length beyond
  the birth record; (b) an explicit `system.kind` on the form; (c) a node kind
  in figwal. (b) is a lie waiting to happen (mutable), (c) reopens the node-kind
  work just retired. (a) is derivable and needs no new state: prefer it unless
  it proves too slow for a listing, in which case cache it in `_meta`.
- `fig form <id>`: its state (the nested tree `fig state` prints).
- `fig form listen <id>`, already works; make it accept a form id.
- `fig form fork <id> [-O …]`: `ForkWith(form, 0, patch)`. Forms duplicate
  state on fork; no shared prefix, no stump.
- Attendance: a form is attendable independently. `fig attend <form-id>` binds
  the shell to it, and the pid-binding surface should not care which kind it is.

## Part 3: Binding

**`bind` is the canonical verb for materializing a figaro from a form.**

- `fig bind <form-id> [-O <dressing>]`: the figaro is FORKED FROM THE FORM at
  its current version. That fork carries at minimum an `aria_id` patch, and
  optionally an outfit on top. Binding makes that form version behave like a
  stump: the aria inherits the form's records as its prefix.
- A figaro may be bound to a form that has history from BEFORE the binding -
  that is the normal case, not an edge case.
- `fig new` is then a special case of bind: bind the null form (or a stump).
  The CLI may not expose null-form binding today; it should eventually.
- **`fig send` on an attended UNBOUND form** forks a figaro from the form at
  that point and applies the loadout patch as if the form were a stump: the
  same shape as unattended `fig send`, which forks the default outfit.

## Part 4: Roles (the duck type, not the observation)

A **role** is any form carrying a `cast-aria` member, expected to be a string.
No new type, no new verb: the presence of the key IS the type.

- The CLIENT resolves it: on `fig send` / `fig listen` / `fig fork` against a
  form id, fetch the form, read `cast-aria`, and use that value as the aria id.
  So `fig send <role-form>` reaches whatever aria currently holds the role.
- This is the 90% of the succession problem: a figaro nearing its context
  ceiling mints a standby from a fork, and the role's `cast-aria` is repointed
  at the successor. Everyone addressing the role follows.

**Why it matters** (the failure it fixes): bound forms fork WITH their figaro
today, so after a fork neither copy can tell who the successor is. A role does
not fork: it is a separate form: so it stays a stable name for "whoever is
doing this job now".

## Part 5: OUT OF SCOPE, deliberately

Role OBSERVATION: a figaro seeing patches to a role in its own history, carrying
a cursor to the role form the way it carries one to its bound form, with the
rule that a fork inherits the role relationship but NOT a private copy of it -
patch a bound form and only the fork sees it; patch a role and every figaro cast
in it sees it. And `fig cast <aria-id> <form-id>`.

Gluck: "that requires a bit of thought." Do not improvise it.

---

## Part 6: Testing, per standing expectations

- Minimal green, maximal red. Some green is expected here; do not be shy.
- `go test ./...` and `-race` on store/figaro/cli/actor/angelus.
- A real pty run of every new CLI surface, in an isolated `nix develop .#sandbox`
  (NOT `.#default`: it inherits the real store).
- The twelve-aria stress recipe in `skills/tmux-testing.md`, re-run.
- `-count=6` benchmarks with the spread reported, before and after. A single
  pair of numbers is not evidence; if a cost is O(something), show the slope.
- Migrate a COPY of the real store, never the live one.

---

## Part 7: The mistakes that cost the v0.23.0 branch time

Not general advice. Each of these happened, in order, on the branch this one
descends from, and each has a check that would have caught it.

**1. Re-scoping in silence.** Asked for A and B, I judged B too large for my
remaining context, did A, and presented a handoff note for B as though it were a
deliverable. The scope call may even have been right; making it silently was
not. **Check: when the budget will not cover the ask, say so IN THE TURN YOU
NOTICE, and name the trade. Never let a note stand in for work that was
requested.**

**2. Deleting something without measuring what it bought.** I removed outfit
stumps, and with them a shared rendered prefix and the provider's per-node
translation cache: having measured only the FOLD (568µs, unchanged) and never
the thing being thrown away (bytes per aria, and a cold prompt cache on every
new conversation). Gluck caught it, not me, and the reversal cost more than the
removal. **Check: before deleting a mechanism, write down what it buys and
measure THAT. If you cannot measure it, you cannot argue it is not needed.**

**3. Attributing a cost to code the benchmark never calls.** I blamed a +6.2%
fork regression on `ForkWith`. `BenchmarkFork` calls `Fork` + `ApplyForm` and
reaches `ForkWith` never; the cost was in the apply half, which is paid on EVERY
form write rather than once per hand-driven fork, a different frequency and a
different fix. **Check: before attributing, confirm the benchmark's call path
actually reaches the function you are blaming. A profile or a `-run` of the
split halves takes two minutes.**

**4. Quoting a point estimate for an O(n) change.** "40.45µs -> 17.84µs" was an
artifact of the benchtime, because history length IS `b.N` in that benchmark.
Two people ran the same fix and got different headlines. **Check: if a cost
scales with anything, report the SLOPE across sizes, not one pair.**

**5. A synthetic benchmark that understated a real defect.** The same fix hid an
O(history) slice copy on every form commit. It looked like noise at small N.
**Check: for anything on the write path, run it at 200/1000/3000 and look for a
curve, not a number.**

**6. A comment that said the opposite of its code.** `actor.Queue.Close` claimed
queued items are dropped; the code drains them, and it MUST: callers block on a
reply. A "fix" to match the comment would have hung every writer. **Check: when
you document a behaviour, run it. Two tests, both shapes.**

**7. One answer for two different failures.** The form mirror answered a schema
mismatch with the same "resync" a version gap gets: a gap is transient and
re-reading cures it, a mismatch is permanent and re-reading is one RPC per frame
forever, and it was invisible, because only the gap branch counted. **Check:
when a function returns a bool, ask whether two callers want different things
from `false`.**

**8. Believing a test over a running binary.** The suite was green while the
birth path leaked the raw `layers` directive onto every board. It surfaced by
reading a real `fig state` after a real `fig new`. **Check: exercise every new
surface through the real binary, and READ the output. Green tests are not a
demonstration.**

**9. A worktree binary reports no revision.** `go build` in a worktree leaves
`figaro version` = `unknown` (Go's VCS detection needs `.git` to be a directory,
and a worktree's is a file), so the CLI/daemon handshake can only warn, and an
old daemon will happily answer a new client. **Check: always
`-ldflags "-X github.com/jack-work/figaro/internal/cli.commit=$(git rev-parse HEAD)"`.**

**10. A test shell that started the WRONG daemon.** An interactive tmux pane's
prompt integration invoked the installed `figaro`, which autostarted a 0.22.1
daemon in my isolated `FIGARO_RUNTIME_DIR` and answered my new client. Twenty
minutes lost to a "bug" that was two binaries. **Check: isolate all three of
`FIGARO_CONFIG_DIR`, `FIGARO_STATE_DIR`, `FIGARO_RUNTIME_DIR`, and drive plain
verbs from a non-interactive shell. `nix develop .#default` INHERITS the real
environment: use `.#sandbox` or `.#clean`.**

**11. A command silently shadowed by an alias.** A new top-level `form` command
was swallowed because `state` already aliases `form`, and its arguments were
read as an aria id, an error that looks like a missing aria, not a routing bug.
**Check: a canary that fails when a registered name collides with any existing
command's aliases.**

**12. Editing a file another aria was mid-edit in.** Two arias share this
repository. **Check: `git status` before you start, and if another aria's work
is in the tree, land it as its own commit or leave it alone: never fold it into
yours, never stash it without saying so.**

### The one-line version

Green tests are not a demonstration; a benchmark is not evidence until you know
what it calls; and when the budget will not cover the ask, say so out loud.

---

## Part 8: KNOWN DEFECT: `@` completion shows the catalog, not the form

Reported by Gluck against v0.23.0, unfixed, and it belongs here because it lives
in the surface this branch touches. **Do this first.**

### The symptom

Completing `@` in an attended shell offers ~25 keys: `cwd`, `datetime`,
`duke-title`, `model`, `root`, `token_budget`, `truncation`, and a run of
`system.*`, and **none of the 27 `skills.*` keys the aria actually holds**.
`fig form <id>` on the same aria shows 45 keys including `skills (27)`.

Two tells that name the cause exactly:

1. **Skills can never be in a static list.** They are materialized from an
   outfit at birth; a hardcoded catalog cannot know them.
2. **It offers keys the board does not have.** `truncation`, `token_budget` and
   `system.verbosity` are not on Gluck's form. They are entries in
   `form.WellKnownKeys()` (`internal/form/known_keys.go`): documentation of
   keys figaro understands, not a statement about any aria.

So what is on screen IS the catalog. `completeFormKeys` adds the catalog and
then adds `softFetchLiveKeys()` on top; the live half is coming back empty.

### Why it is empty: check in this order

`softFetchLiveKeys` (`internal/cli/complete_form.go:62`) returns **nil on
every failure**, by design: completion must not autostart a daemon, prompt, or
block. It has five nil-returning exits: dial the angelus, resolve the pid
binding, dial the aria, `fcli.Form(ctx)`, and the decode.

1. **Mixed versions (most likely).** v0.23.0 renamed the read method
   `figaro.form` -> `figaro.form` (and `aria.form` -> `aria.form`).
   A v0.23 CLI against a 0.22.x daemon gets *method not found* at the
   `fcli.Form(ctx)` line and falls back silently. Completion runs whatever
   `figaro` is on PATH, which is often the INSTALLED binary while testing a
   worktree build: so this can be true even when `fig form listen` works.
   Confirm: `figaro version` on both sides; `figaro stop` and retry.
2. **Regression in the live path.** The same commit that removed
   `outfitFallbackKeys` (the unattended fallback that folded the default outfit
   CLIENT-side: server state a client must not read) touched this file. Verify
   `softFetchLiveKeys` still resolves the binding and that `resp.Snapshot.All()`
   is being drained into `out`.
3. **Binding.** `resolveBinding(ctx, acli, shellPID)` must find the shell's
   aria. A completion running under a different pid (a subshell, a wrapper)
   resolves nothing and is indistinguishable from a broken fetch: see below.

### The real defect is the silence

Whatever the immediate cause, the design fault is that **five distinct failures
share one answer (`nil`), and the caller renders a plausible wrong result on top
of it.** "No aria bound" and "the daemon refused the method" are different facts
and a human had to notice that skills were missing to discover either. This is
the same shape as the form mirror's schema-mismatch bug fixed in v0.23.0: one
return value serving two meanings, and the invisible one winning.

**Fix the silence, not just the fetch:**

- Distinguish *unbound* (offer the catalog: correct, it is all that is knowable)
  from *fetch failed* (a wrong answer dressed as a right one).
- On a failed fetch, completion still must not block or prompt: but it can
  decline to offer the catalog, or mark the list, or write one line to the
  completion debug log. Silently substituting documentation for state is the
  behaviour to remove.
- A test: an attended aria whose form carries `skills.foo` must complete
  `skills.foo`. It would have failed the day this broke.

### While you are there

`fig form <id>` is the honest view of what a board holds; the catalog is the
honest view of what figaro understands. They are different questions and the
completion currently answers the second while appearing to answer the first.
Consider marking catalog-only entries in the completion display so the
difference is visible at the point of use.

---

## Part 9: `fig form listen` keymap: follow transcript mode, do not invent

The TUI shipped in v0.23.0 with the minimum (`j`/`k` move, `enter` expand a
branch, `y` yank, `e`/`d` page). It should grow the movement vocabulary the
transcript pager already teaches, so a reader who knows one knows the other.
Read the pager's key handling first and mirror its idiom rather than inventing a
second dialect in the same binary.

Wanted:

- **`gg` / `G`**: top and bottom. `gg` is a two-key sequence, so it needs the
  pending-prefix handling the pager already has; do not hand-roll a timer.
- **`u` / `d`**: up and down (half-page, vim's `C-u`/`C-d` sense).
  **Reconcile with the existing `e`/`d` paging**: `d` is currently page-down, so
  `u`/`d` as half-page either replaces that pair or collides with it. Pick one
  and say so in the footer; two bindings for one motion is how a keymap rots.
- **String search with highlight**: search VALUES, not just keys, and highlight
  the match in place. A form holds skill bodies measured in kilobytes, so search
  is the only way to find anything in one.
- **`enter` on a LEAF expands its text.** Today `enter` only opens branches and a
  leaf renders as a single clipped line (`formValuePreview` cuts at 120 chars).
  A leaf holding a whole credo or a skill body needs a way to read it: expand
  in place, or a pager pane. This is the one that makes the TUI usable on a real
  form rather than a toy one.

Everything else the pager offers for free (scrollback discipline, the frame-rate
ceiling, the deferred right-edge wrap) is worth taking rather than
re-deriving: see `skills/tmux-testing.md` for how to verify any of it honestly,
because a keymap is exactly the kind of thing that looks right in a unit test and
is wrong on a screen.
