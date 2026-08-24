# Insert mode: a composer in the transcript

> **STATUS: PLAN, NOT BUILT, AND NO LONGER FIRST.** Written 2026-08-21 from two
> illustrations (`/tmp/illus`, `/tmp/illus-fork`). Nothing here is in the tree.
>
> **Read [transcript-subject.md](transcript-subject.md) before this file.**
> Gluck reframed the work on 2026-08-23: the transcript cannot change which aria
> it shows, and everything here that forks, follows or addresses another aria is
> blocked on that missing primitive. This file is now the LAST phase of that
> one, and it shrinks a lot in the process -- the box becomes the shared line
> editor with `maxRows 4`, and its default acceptor becomes `:send` on the
> current subject.
>
> Superseded here, in detail there: the box is one line by default and at most
> FOUR (not five); Esc keeps the box VISIBLE and only leaves insert mode; the
> editor is `bubbles/textarea` driven headless, so it is literally the control
> `composePrompt` uses and its emacs/bash keys come with it. The chord table in
> §4 stands as decided.

`i` opens a text box in the transcript's footer stanza. Enter sends it to the
aria you are looking at. With a node selected, `f` opens the same box with a
fork intent attached, and Enter forks there, rebinds the transcript to the
branch, and attends it.

## The rule this design is built around

There is a shipped smoke case, `TestSmoke_LettersAreKeybindingsNotText`, whose
comment is the constraint:

> CAUGHT, and then REVERTED: an in-view steer composer was built that made
> every printable character start typing a draft. Nobody asked for it, and it
> cost ten keybindings: `k` opened a text box instead of scrolling. The user's
> rule is that there is nothing in the UI to steer: a message is a steer purely
> because of WHEN it is sent.

So:

**1. The box is MODAL, and the mode is the whole difference.** `i` is the door.
Outside insert mode every letter is still a motion, and the smoke case above
must keep passing untouched. It probes the INLINE view, so insert mode does not
exist there at all — the transcript is the only surface that grows a box.

**2. There is still nothing in the UI to steer.** The composer has one verb:
*send*. Whether the result is a new turn or a steer is decided server-side at
the drain, by timing, exactly as it is for `figaro send`. If a "steer" button
ever appears in this work, the work is wrong.

**3. Test the path of someone who does not know the affordance exists.**
Trap 6 in `@skills.tmux-testing`: three investigators tested the reverted
composer by pressing its trigger key first and all three pronounced it sound.
Every case below that types must have a twin that types *without* pressing `i`.

## What already exists (and what the feature should therefore not build)

Six findings from reading the tree. Each one deletes a chunk of the obvious
implementation.

| I expected to build | It is already there | Where |
|---|---|---|
| a submit path | `fcli.Qua(ctx, text, buildPromptForm())`; `interactiveInput` already holds `fcli` in both `send` and `listen` | `stream.go:357`, `listen.go` |
| a "message pending" state | `Store.pending []Pending` + `Store.Pending()`, **with zero consumers** — range-store phase 4, built and unused | `livelog/aria/store.go:84,797` |
| a modal keymap | `keyMode` + `keyModeSet`, a declarative table, one row per binding, with well-formedness tests | `keymap.go` |
| a node coordinate for the fork | `nodeRef{turn,index}` selection via `^N`/`^P`, and `<id>:<turn>.<node>` parses end to end | `transcript_selection.go`, `send.go` |
| CSI-u modifier decoding | `parseModifiedKey` reads `code;modifiers` generically | `key_input.go:50` |
| **a client cache of a fork's shared prefix** | **the SERVER already shares it**: `A FORK'S COMPOSED PREFIX IS ITS ANCESTOR'S RUNS` (e54b6299) — the prefix is read through the tree's lineage walk, never copied | `angelus.composeTurns` |

That last row is the important one and it is answered in §7.

## 1. The composer is a value, not a program

`composePrompt` (the `figaro send` box) is a **bubbletea program** — it takes
the terminal, runs its own event loop, and returns. It cannot be reused here:
the transcript owns the alt screen, paints its own frames from `compose()`, and
reads its own input. Two programs cannot hold one terminal.

So the composer is a **widget**: state plus a pure render.

```go
// composer is the transcript's text box. It owns RUNES, not bytes: see the
// literal-input rule below.
type composer struct {
    runes  []rune
    cursor int      // rune index, 0..len(runes)
    intent forkAt   // zero = an ordinary send; set = fork here first
}

// rows wraps the buffer to w columns and returns at most composerMaxRows,
// scrolled so the cursor is visible. THE ONE AUTHORITY on how tall the box is
// (see §2); nothing else may count them.
func (c *composer) rows(w int) []string
```

**It must be rune-aware and width-aware, and the existing boxes are neither.**
`searchLiteral` and `jumpLiteral` are `if b >= 0x20 && b < 0x7f { q += string(b) }`
— ASCII only. That is survivable for a coordinate; for a *prompt* it is not, and
it is already a live bug in search. Measured, not inferred — typing `café` into
the `/` box one byte at a time, through the real dispatcher:

```
query = "caf" (typed "café")
```

The two bytes of `é` are each rejected by the `< 0x7f` test and **the box
silently swallows them**: no error, no bell, a query that is not what was typed.
Every non-ASCII search in this program has always been wrong this way. Which
leads to the refactor:

> **ONE TEXT BOX PRIMITIVE, THREE USERS.** Build `composer` so the search box
> and the jump box are the one-row case of it. That deletes two hand-rolled
> `+= string(b)` paths, fixes non-ASCII search as a side effect, and means the
> byte-vs-rune bug the tmux skill records (trap 5) has exactly one place left
> to live.

Width comes from `runewidth` + the repo's own `ambiwidth.go`, which already
solved the East-Asian ambiguous-width question for the renderer.

**Paste is part of the contract, not a nicety.** A prompt is pasted far more
often than it is typed. Nothing in the tree enables bracketed paste today
(`\x1b[?2004h`); without it a pasted newline reads as Enter and **sends a
half-written prompt**. Bracketed paste is therefore in the same phase as the
box, not a follow-up.

## 2. The footer stanza gets one owner

Today the bottom of the frame is assembled in four places:

- `footLines()` — panel rows (`?`/`!`/`Q`)
- `footerRows()` — the rule and the status line
- `layout(foot int)` — `body = t.h - 2 - foot`, minus one more while following
- `renderFrame()` — writes `screen[t.h-2]`, `screen[t.h-1]`, and the panel rows

Four sites, magic `-2`/`-3`, and a height passed as an `int` argument. Adding a
**variable-height** stanza to that is how you get an off-by-one that only
appears at `h=5` with three rows typed.

The repo already learned this lesson in the body:

> ONE AUTHORITY ON HOW TALL AN ENTRY IS: `lineEntry.height`. Line space is
> advanced by asking the entry, not by re-deriving it here: the two disagreeing
> is how a gap could be one row in the index and several on screen.
> — `transcript_index.go`

Do the same at the bottom:

```go
// footer is the whole bottom stanza, top to bottom: panel rows, the rule, the
// composer's rows, its closing rule, the status row. ONE function builds it and
// layout() asks it how tall it is, so the body's height and the stanza's height
// can never disagree.
func (t *transcript) footer() []string
```

`layout()` becomes `body = t.h - len(t.footer())` (keeping the follow row), and
`renderFrame()` copies the stanza in. The illustration's shape falls out:

```
────────────────────────────── aria e9ff853e · 741–778/778+ live ───   rule
> My input would go here.                                              composer (1..5)
────────────────────────────────────────────────────────────────────   closing rule
mantra · ctx 397.3k/1.0m 39.7% · cost 190.6k tok · 17:10:10            status
```

**The floor is the pane, not the box.** At `h < 4` `renderFrame` already
returns early; the composer must clamp to what is left rather than assume five
rows exist, and a pane too short for a box refuses insert mode with a note
rather than painting over the transcript.

## 3. Three states, and Esc walks back through them

From `/tmp/illus-fork`, read as a state machine:

| state | rows | status row | draft |
|---|---|---|---|
| **normal** | none | mantra · ctx · cost · time | kept in memory |
| **insert** (`i`) | 1–5 + closing rule | the composer's own controls | live |
| **parked** (Esc) | none | the composer's controls, so they are discoverable | kept |
| normal (Esc again) | none | back to mantra · ctx · cost | kept until disconnect |

The draft is **cached in CLI memory and discarded on disconnect** — never
written to disk, never sent, and `i` resumes it where you left off.

*Interpretation to confirm with Gluck: I read "retain the status bar so that
shortcuts and controls can be restored" as the parked state showing the
composer's key hints. If it instead means "keep the input row visible but
inert", the table changes and the plan is cheap to amend.*

## 4. The keymap grows one mode

`modeInsert` joins `keyMode`; `inInsert` joins `keyModeSet`; `t.mode()` gets a
case above `modeTranscript`. Then rows in the one table, which is where every
binding in this program is declared and where the well-formedness tests can see
them.

| key | mode | action |
|---|---|---|
| `i` | transcript | open the box (`staysInline` in incipit: the inline view has no box) |
| `f` | transcript **with a selection** | open the box with a fork intent at the selected `turn.node` |
| Esc / `^[` | insert | park (draft kept) |
| Esc / `^[` | parked | back to the ordinary status row |
| **Alt+Enter** | insert | **submit** — send, or fork-and-follow when an intent is set |
| **Enter** | insert | newline |
| **Ctrl+F** | insert | submit **without attaching**: fork in the background, stay where you are |
| ^A/^E/^K/^U/^W, arrows, Backspace | insert | the readline motions people already have in their fingers |
| anything printable | insert | **text**, through the one literal fallback |

### Why these chords: the prior art, and where we diverge from it

Four terminal apps with our exact shape were consulted before any row was
written.

| app | send | newline | mode model |
|---|---|---|---|
| **iamb** (Matrix, "for Vim addicts") | `Enter`, **from Normal *or* Insert** | `<C-V><C-J>` | vim-modal |
| **weechat** | `Enter` | `Alt+Enter` (community-standard rebind; `/input insert \n`) | non-modal |
| **aerc** (mail, vim-modal) | `:send` — an ex-command | ordinary editing | vim-modal |
| **figaro's own `composePrompt`** | `Ctrl-D` | `Enter` | its own screen |

The dominant chat convention is **Enter sends**. We deliberately do not follow
it, for four reasons that are all specific to what this box is attached to.

**1. A pasted newline is an Enter, and the failure is not hypothetical.** With
no bracketed paste, a terminal delivers a pasted `\n` as `0x0d`. A shipped
agent CLI has this bug filed against it right now, in a UI with our exact
shape — a steering queue behind a prompt box:

> *fix(cli): multiline paste splits into separate steering messages when agent
> is running.* "When pasting multiline text into the prompt while the agent is
> running, **each line is sent as a separate steering message** instead of the
> full text as one message. Root Cause: without bracketed paste support…"

With `Enter` as a newline, that class of bug **cannot be written**. Bracketed
paste stops being a correctness requirement and becomes an optimisation (it
still buys us "paste does not fire the 5-row grow animation per line").

**2. The two mistakes are not the same size.** A stray newline costs a
Backspace. A stray send costs a provider round trip, a turn in the log, and a
steer the agent will act on — and there is no unsend. When the failure modes
are this asymmetric the safe key gets the common press.

**3. We are modal, so we do not need Enter to do double duty.** iamb makes
`Enter` send *from Insert mode* — it spends the vim invariant to buy a chat
convention. It can afford that because its box is one line. Ours grows to five,
which is a declaration that multi-line is a first-class case; and once `i` and
Esc exist, "the key that ends the message" does not have to be the key that is
already busy making lines.

**4. `Alt+Enter` is legible in exactly the right way.** It reads as *"Enter, but
final"*, it sits under the same finger, and weechat has already taught a
generation of terminal chatters that Alt+Enter is "the other Enter" (there, the
polarity is reversed — our newline is their send — which costs one line of help
text and no ambiguity, because both are Enter-shaped).

### The whole insert-mode map, and what it disturbs: nothing

Everything below lands on a chord that is **free today**, so no existing binding
is rebound and the preserved set (`^N`/`^P`, `j`/`k`, `u`/`d`, `i`, `f`) is
untouched. Insert mode is where vim itself binds almost nothing, so the readline
conventions can be taken whole without arguing with the normal-mode map.

| chord | byte | action | precedent |
|---|---|---|---|
| `Alt+Enter` | `1b 0d` | **submit** — send, or fork-and-follow | weechat's "other Enter" |
| `Enter` / `^J` | `0d` / `0a` | newline | vim insert mode |
| `Ctrl+F` | `06` | **submit detached** — fork, do not follow, do not attend | free; pairs with `f` |
| `Esc` / `^[` | `1b` | park (draft kept) | vim |
| `^W` | `17` | delete word back | readline **and** vim insert |
| `^U` | `15` | delete to line start | readline **and** vim insert |
| `^K` | `0b` | kill to end of line | readline |
| `^A` / `^E` | `01` / `05` | start / end of line | readline |
| `^V` | `16` | quote the next key literally (so `^V^J` is a raw LF) | vim, and iamb's newline |
| Backspace / DEL | `08` / `7f` | delete back | already handled for the other boxes |
| `^N` / `^P` | `0e` / `10` | **still node selection** — Gluck's preserved pair | unchanged |
| `^C` / `^D` | `03` / `04` | **still interrupt / detach** — unchanged, deliberately | see below |

**`^D` is NOT a second send chord, although figaro's own `composePrompt` says
"ctrl-d send".** Internal consistency loses this one to safety: `^D` is the
universal "get me out of this terminal program", it is bound to detach three
keystrokes away in every other mode, and a user hammering it to escape must
never discover that it *sent* their half-written draft instead. One submit
chord, and it is the one that means nothing else.

**`^F` is live only when a fork intent is set.** Without one there is nothing to
detach from — an ordinary send is already unattached — so it reports that in the
status row rather than quietly doing what Alt+Enter does.

**THE HAZARD THIS CREATES, which must be a test before it is a row.**
`Alt+Enter` is `\x1b` `\r` — an ESC byte followed by a CR. `parseModifiedKey`
recognises `\x1b[`-prefixed CSI and two hand-listed `ESC`+ctrl cases, and
nothing else; an unrecognised `\x1b` falls through to Esc's own binding. In
insert mode that reads as **park, then a stray newline into a box that is no
longer showing** — the submit silently becoming its opposite, which is the worst
failure shape a send key can have. `\x1b\r` needs its own row in the same commit
as the binding, and a pty case that sends the two bytes as ONE read, because
that is how the real key arrives and a test that sends them separately would
pass while the key was broken.

Measure the terminal before believing any of this: trap 11 of the tmux skill is
an entire A/B that ran the same binary twice. Send the literal bytes and read
back what arrives.

## 5. Send, and the message you can see immediately

```
Enter → composer.text() → (off the render lock) fcli.Qua(ctx, text, buildPromptForm())
```

Off the lock, like the queued-panel fetch already is: a five-second RPC must not
freeze a frame.

**The submitted prompt must appear at once**, or the box looks broken for as
long as the round trip takes. That is what `Store.pending` is for, and it has
been sitting unused since range-store phase 1:

> **Pending** is a prompt that has been submitted but not yet classified by the
> drain. It has NO server coordinate, because only the drain can assign one.
> …On acknowledgement it resolves exactly one of two ways: **inquiry**, acquires
> a turn id, merges into the head range; **steer**, becomes a node inside
> `open`. Both are "acquire a coordinate and move". **The client MUST NOT guess
> which will happen.**
> — `range-store.md`

So: push a `Pending` on submit, render it after the head range in its own dim
style, and let it dissolve when the real record arrives with its coordinate.
This is phase 4 of a design that is already written down — not new machinery,
just its first consumer.

## 6. Fork, and the plumbing it needs

`f` with a selection sets `composer.intent = forkAt{turn, node}`. The box then
draws the prefix from the illustration, in `term.StateDim` — the "∆ Figaro saw"
register, which is what Gluck's `*` marks:

```
*forking at 12.4* > My input would go here.
```

Enter then does, in order: **fork → rebind the transcript → attend → send**.

**The plumbing gap: the transcript has no angelus.** `interactiveInput` holds
`fcli *sdk.Aria` — one socket, one aria. Fork (`acli.Fork`), attend
(`acli.Bind`) and the branch's endpoint all live on the **angelus** door, and
`listen` never dials it. So the input loop grows an angelus client, dialled
lazily on the first fork so the ordinary listen path pays nothing.

The three verbs already exist and are exercised by the CLI:
`waitForFork(ctx, acli, id, at, dressing)` → `bindBinding(ctx, acli, shellPID, id, 0)`.
`at` is `forkPoint{turn, node, hasNode}` — **the coordinate the selection already
holds**, and the node fork lands at `min(node.LTs)-1` (`compose.ForkLT`), which
the previous commit built and pinned.

Which id do we follow? `ForkResponse` returns `{Parent, Continuation,
Alternative}`. An interior fork freezes the parent, `Continuation` carries on
the original conversation, and `Alternative` is the fresh branch — so the new
prompt goes to **Alternative**, and that is what we attend and display.
*Confirm with Gluck.*

`Ctrl+Enter`/`^F`: fork, send to the branch in the background, **do not** rebind
and **do not** re-attend. One line of difference, and it is the reason the two
paths must be one function with a `follow bool`.

## 7. Rebinding, and the prefix cache

### The seam

```go
// switchAria tears the aria-scoped world down and builds it again around id.
// EVERY path that changes which aria the transcript shows goes through here.
func (in *interactiveInput) switchAria(id string) error
```

It closes `fcli`, dials the branch's endpoint, mints a fresh `aria.Client`,
re-seeds from the tail, and repoints `figaroID`. That is the "true reload"
Gluck is content with for a first cut, and it is honest: it is exactly what
`figaro listen <branch>` does.

### And then the cache, which is smaller than it sounds

Gluck: *"a tree shaped cache of common prefixes could arguably be engineered on
the client."*

**The server already built the tree**, in e54b6299:

> `Angelus.composeTurns(node, fromLT, toLT)`, keyed by node and backed by the
> store, so it answers for a live aria, a dormant one, and an ANCESTOR NOBODY
> HAS OPENED — which is exactly the read a fork's inherited prefix makes.
> `TurnCache.put` now skips any turn below its node's fork base: **the prefix is
> read through tree's lineage walk instead of copied.**

The consequence for the client is the whole design: **for every turn below the
fork base, the branch's `figaro.read` returns the ancestor's own composed runs.
Same turn ids, same node ids, same bytes.** The client's row cache is already
keyed by `sliceKey` = `(turn, node)` — *the identical key*. The rows are already
correct for the branch. The only reason a fork costs a reload today is that
`switchAria` throws the whole client away.

So the cache is not a new structure. It is a **retention rule**:

> On `switchAria` into a branch of the aria we are already showing, keep every
> range and every cached row **strictly below the fork turn**, and drop
> everything at or above it.

Two rules make it safe, and the server has already paid for learning both:

- **Snap the base to a turn boundary.** e54b6299's canary: `ForkAt` takes an
  interior LT, so a fork cutting mid-turn leaves the child "a turn made of the
  ancestor's opener and its own continuation — same turn id, different content".
  Without the snap the child is served the parent's answer for its own turn.
  On the client this is free: we fork at a coordinate we chose, so the boundary
  is `forkTurn`, and "keep turn < forkTurn" is expressible in the client's own
  coordinate with no LT translation at all.
- **Drop at-or-above, always.** The turn you forked at is the turn that
  *differs*. Keeping it is the exact bug the server hit.

Cheap, and it explains why it can wait: phase 6 changes one function, adds no
type, and the phases before it are what make it observable.

## Phases

Each shippable, each with the canary that proves it.

| # | what | canary |
|---|---|---|
| 1 | `footer()`: one owner for the stanza, no behaviour change | revert it and watch a `h=5` frame overwrite the status row |
| 2 | the `composer` widget + bracketed paste; **search and jump become its one-row case** | search for `café` — fails today, passes after |
| 3 | `modeInsert`, the keymap rows, the three-state Esc walk | type `j` without pressing `i`: it must still scroll (the reverted-composer case) |
| 4 | Enter → `Qua`, and `Pending` gets its first consumer | submit against a slow daemon: the prompt is on screen before the RPC returns |
| 5 | `f` → fork intent, angelus dial, `switchAria`, follow / background | fork at `12.4`; the branch's turn 12 must hold the parent's nodes 0..3 and none of node 4 |
| 6 | prefix retention across `switchAria` | count `figaro.read` calls across a fork-and-follow: it must not re-read the shared prefix |

Phases 1 and 2 are pure refactors that pay for themselves before any of this is
visible: one is an off-by-one waiting to happen, the other is a live bug.

## Testing standard

`@skills.tmux-testing` applies whole. The four traps this feature walks into:

- **Type one character per read** (trap 5). A composer fed whole strings passed
  while a byte-vs-rune bug mojibaked every non-ASCII character. `typeSlowly`
  exists in the harness for exactly this.
- **Test without the affordance** (trap 6). Every insert-mode case gets a twin
  that never presses `i`.
- **Gate every absence on pager chrome** (trap 3), and read back
  `#{pane_height}` rather than trusting `-y` (trap 1) — a five-row box makes the
  geometry load-bearing, so measure it.
- **Build a stamped binary** (the CLI/daemon handshake refuses a mismatch; a
  40-char stamp is not the 12 the flake writes).

And the fixture trick from the seek work, which costs no tokens: `figaro import`
mints a 120-turn aria from a JSON file in a second, so a pty case can drive a
long transcript without a provider.

## Open questions for Gluck

The chords are decided (§4) and need no ruling. These five do, and each one is
a place where I would otherwise be guessing at intent rather than at mechanism.

**1. The parked state — what does the status row show?** Esc hides the box and
keeps the draft. My reading of *"retain the status bar so that shortcuts and
controls can be restored"* is that the row then shows the composer's own key
hints (`alt-enter send · ^F fork · i resume · esc dismiss`), so the controls
stay discoverable, and a second Esc returns the ordinary mantra/ctx/cost line
with the draft still cached. The alternative reading is that the input row
itself stays visible but inert. Which?

**2. Which id is "the fork" we follow?** An interior fork freezes the parent and
returns three ids: `Parent`, `Continuation` (the original conversation carrying
on) and `Alternative` (the fresh branch). I intend to send the new prompt to
**Alternative**, and attend and display that. Right?

**3. Does `i` do anything in the inline view?** Proposal: **no** — the box
exists only in the transcript, so the inline view keeps every letter as a
keybinding and the shipped smoke case that guards that rule stays untouched.
Pressing `i` inline would then either do nothing, or open the pager and the box
together. Nothing, or open-and-insert?

**4. A parked draft carries a fork intent, and then `^N`/`^P` moves the
selection. Does the intent follow?** `^N`/`^P` stay live in insert mode (they
are preserved), so the selection can move while you type. Following would make
the box prefix change under you — `*forking at 12.4*` → `*forking at 12.5*` —
which is at least visible. Pinning means the intent stays where `f` aimed it and
the selection is just a cursor. I lean **pinned**: you aimed it deliberately.

**5. What happens to a draft when the aria is not idle?** Nothing special is
planned: the prompt goes to `Qua`, and the server decides at the drain whether
it opens a turn or becomes a steer — which is the existing rule and the reason
this box needs no steer affordance. Confirming that you want no warning, no
confirmation, and no difference in the UI between the two.
