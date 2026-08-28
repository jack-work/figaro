# The status bar, the modes, and the drawer vocabulary

> **STATUS: PLAN**, revised 2026-08-28 after review (keel `jack/figaro`,
> commit `dfd64f61`, 12 threads). Written against
> `notes/figaro/ui/status-bar-expected.md`. UI only: nothing here touches the
> wire, the store or the provider layer. It is the tidying that input mode and
> transcript **multiplexing** both need first, and it earns its keep anyway,
> because the bottom two rows are the only thing on screen that is always true.

## The three things wrong today

1. **The bar is assembled from a session, not from a value.** `statusLine`
   takes a lock, formats, sheds tokens by rank and returns a string, against the
   one `*sessionStatus` a process has. **Multiplexing needs N of them**, and a
   pane's bar must render without owning the session behind it.
2. **Two questions share one answer.** `t.inSearch`, `t.inJump` and
   `t.drawer.kind` all describe what is on screen, and `mode()` derives a
   `keyMode` from them for the keymap. Nothing owns "what is open"; each
   consumer re-derives it, and for one frame they can disagree.
3. **The state vocabulary is prose.** `turnLabel()` returns `"completed ✓"` —
   symbol and name fused, no colour, no way to ask for one without the other.
   The requirement is symbols by default and names under a toggle; that shape
   cannot express it.

## 1. `statusView`: a value, and a pure renderer

```go
type statusView struct {
    Drawer  drawerID   // what is open; drawerNone in plain transcript mode
    State   turnState  // §3
    Aria    string
    Mantra  string
    Ctx     ctxUsage   // used, limit, exact
    Alert   string     // the newest unread notification, projected (§4)
    LastAt  time.Time  // when this conversation last moved; verbose only (§5)
    Verbose bool
}

func (v statusView) render(w int) []string   // 1 row, or 3 when w is small
```

Pure, and therefore **golden-testable at every width**, which today's renderer
is not: 40/60/80/120 columns × {plain, drawer open, alert, verbose, no mantra}.

```
── the transcript rule, unchanged ───────────────── aria 3b7aff0a · 1–29/118 ──
𝄚 queue · ✓ · 123abc · test                              9.8k/1.0m (1.0%)
└ drawer ┘ └state┘ └aria┘ └mantra┘                        └─ right-justified ─┘
```

- **Narrow** is **three rows**, not two: left group, blank, right group, all
  left-justified. The blank is in the worked example, and the row count goes
  through the same height accounting as the drawer — off by one here and the
  transcript loses a line.
- **Verbose** wraps both sides, truncates each FIELD rather than the line, and
  **a wrapped row carries no `·`**: the separator joins fields on a row, it does
  not dangle at a break. `LastAt` joins the RIGHT group, beside the context
  figure — both are facts about the conversation rather than about the mode.
- **Shedding is replaced by wrapping.** The rank ladder exists only because
  everything had to fit on one row. The mantra is the last thing to go.

**Leaving the default bar: `cost` and the clock.** The spec says "only input
window/output window by default (plus percentage)". Cost moves to the `!` panel
with the cache buckets it is meaningless without. This is the one subtraction a
reader notices on day one, so it is stated rather than discovered.

**But a time comes back under verbose, and it is a different time.** An earlier
draft of this plan said "the clock belongs nowhere — every terminal already has
one", and that was right about the wrong clock. `startedAt` answers "when did I
open this", which nothing depends on. **`LastAt` answers "is this conversation
stale?"** — and nothing else on the screen answers it, because a pager sitting
on a finished turn looks identical whether that turn ended nine seconds or nine
hours ago. See §5.

## 2. One owner for "what is open"

Keyboard behaviour and visible identity are **different axes** — `help`,
`status` and `queue` share `modePanel` while being three different things on
screen — so they keep two types. But they are not independent: **the drawer
identity is the source, and `keyMode` is derived from it.**

```go
func (d drawerID) keys() keyMode   // command → modeJump, search → modeSearch, panels → modePanel
```

That is the whole change, and it is what makes "one owner" true rather than
aspirational: today `mode()` derives a `keyMode` from `inSearch`/`inJump` while
the drawer tracks the same fact separately, so for one frame they can disagree.
Opening a drawer becomes the only transition, the boxes keep their buffers, and
nothing else reads those booleans.

One table keyed by drawer id gives the glyph and the name.

| drawer | mode glyph | name | selection glyph |
|---|---|---|---|
| queue | `𝄚` | queue | `♭` |
| notifications | `𝄞` | notifications | `♩` |
| command | `∴` | command | — |
| search | `⌕` | search | — |
| help | `?` | help | — |
| status | `!` | status | — |
| message | — | — | — |
| output | — | — | `♭` |
| live (hosted verb) | — | *(the verb's own)* | `♭` |
| *(reserved)* | | input | |

**Every drawer needs a row, including the ones with nothing to show.** If
`drawerID` is the source of `keys()`, then message, command output and a hosted
live view are identities too — they just contribute **no token** to the bar
(glyph `—` means the left group starts at the state) and take `modePanel`.
Completion candidates are not a drawer: they are rows inside the command box,
which is why they do not appear here.

**`⌕` (U+2315), not `🔍`.** The bar is width-sensitive and every other glyph
here is single-width; the emoji magnifier is double-width and font-dependent,
which would make the left group's width a property of the reader's terminal.

**Help is a first-class drawer identity**, at the same level as queue, command
and notifications: its own row, its own glyph, and `?` SWITCHES TO IT from every
mode except command, where `?` is text. Its keyboard behaviour is still
`modePanel` — being first-class is about identity, not about inventing a
keyMode nothing needs.

**Two consequences, stated because they are real costs.**

`?` is text in the SEARCH box too, and this rule takes it: searching for a
literal `?` has no route here. The alternative — exempting search as well —
leaves help unreachable from the box where a reader is most likely to be lost,
so the key wins and the literal loses.

**In the command box, help is one `Esc` away**, and that is the answer to "help
should always work" there. It is an exception with a route, not a hole: `?` must
stay literal where a command is being typed (`:send -- what?`), and every other
mode reaches help with one key. The hint row says so, which is why that row is
not gated on verbose.

## 3. `turnState`: symbol, name, colour

```go
type turnState uint8   // thinking, done, hup, error, idle
func (s turnState) symbol(tick int) string  // thinking animates; the rest are static
func (s turnState) name() string            // "" for thinking: it has none
func (s turnState) style() term.Style       // hup gray, error red, rest default
```

| state | succinct | verbose | when |
|---|---|---|---|
| thinking | animated | *(no name, by requirement)* | a turn is in flight |
| done | `✓` | `done ✓` | turn complete, no hup |
| hup | `!` | `hup !` (gray) | user hangup |
| error | `✗` | `error ✗` (red) | hup due to error |
| detached | `⠸` | `detached ⠸` | this CLI left; the turn continues |
| idle | `𝄐` | `idle 𝄐` | **the catch-all**: none of the above |

**`detached` keeps its meaning and loses its animation.** The first draft of
this plan deleted it — a spinner that never turns again, since nothing repaints
after a detach. But the animation was the defect, not the fact: "I left and the
turn is still running" is precisely when the follow hint applies, and `idle` is
defined as the catch-all for conditions *not otherwise known*. A static glyph
keeps the truth and drops the lie.

**Hup is a state, not an error**: the spec says so, and it is why hup is gray.

## 4. Notifications: a sink, a drawer, and nothing clever

**Phase 1 is CLI-local, and says so.** A ring in the pager process cannot see
the daemon's or the agent's `slog` — they are different processes — so this
phase captures the CLI's own records plus anything already arriving over the
wire. Claiming "all warnings and errors" without a transport would be a lie.

- **A bounded ring** (`logring` exists) behind a `slog` handler.
- **`N` toggles** the drawer; from another drawer it replaces it, by the
  one-transient-region rule. `^N`/`^P` move, `y` yanks, **no delete**: a log you
  can edit is not a log.
- **`Alert` replaces the status bar's `notice`, and it is TRANSIENT.** There is
  no second error channel: `setNotice` is deleted and every caller — including
  the ordinary confirmations like `sent` — posts a notification instead. The
  newest one occupies the **first left-hand slot** of the bar, ahead of the
  drawer token, and it leaves on its own:

  - it expires after `ui.notice_ttl` (default **10s**), or
  - a newer notification displaces it, whichever comes first.

  Nothing about it is tied to the drawer being read. A bar item that waits to be
  dismissed is a bar item that is still there tomorrow.

**This is the one piece with a liveness requirement**, and the mechanism is
already in the building. Everything else in the pager repaints because something
arrived — a key, a frame, a resize — so an alert that expires on a clock would
otherwise vanish on the next keypress, which reads as randomness rather than as
a timeout.

**Use the 11 Hz ticker, not a new timer.** `mustPromptFigaro` and `tailFigaro`
already keep it alive and call `lt.tick()` under the render mutex even when
nothing is animating; having `tick()` retire an expired alert gives expiry
within ~90 ms with no second scheduler and no teardown path to get wrong.

**And keep the clock out of `render`.** Expiry is evaluated where the tick is,
not inside `statusView.render(w)` — otherwise the pure renderer this section
promised becomes a function of the wall clock and its goldens become flaky.

**Next steps, named:** a notify transport so daemon and agent records arrive
(the aria socket already carries frames; this is one more method); level
filtering in the drawer; an unread count in the bar (cut here because its
reset semantics are undefined, and "do something simple for now"); an
alternative sink; nvim-style levels with timeouts.

## 4a. One picker, three drawers

The completion menu already is the thing the queue and notifications want to be:
a fixed-height window over a longer list, one selected row, `^N`/`^P` to move,
`Esc` to dismiss, and an honest marker for what is out of view. It is written
once, inside the command box, and the queue drawer has a second, poorer copy of
half of it.

**Extract it.** A `picker` owns rows, a cursor, and a page:

```go
type picker struct {
    rows   []drawerRow
    cursor int            // -1: nothing selected
    top    int            // first visible row; the window slides to keep cursor in view
}

func (p *picker) move(d int)            // ^N / ^P, and repeated Tab
func (p *picker) window(h int) []string // the visible slice + the "… N more" marker
```

Three consumers, one behaviour: **completions** (columns, inserts on move),
**queue** (rows, `x` drops), **notifications** (rows, `y` yanks). What differs
is the row renderer and the verb keys, which stay with each drawer; what is
shared is everything a reader's fingers touch.

**The truncation marker is `cmdkit.AndMore`** (build step 4), so "… 749 more"
reads identically in a picker, in `form show` and in `figaro ls`. A list that
lies about its own length is the one failure mode all three share.

## 5. Verbose (`^V`), and the paste it must not eat

`^O` (verbose tool output) is a different axis and stays. `^V` toggles **the
bar's verbosity**: state names, `system.model`, the hint row, and the time of
the **last interaction**.

**`LastAt` is a full datetime, `01/02/06 15:04:05`** — `08/28/26 12:47:31`, not
a bare wall clock and not a relative "3m ago". A date because the question it
answers is "is this stale", which a time alone cannot answer once a pager has
been open across midnight; and absolute rather than relative because a relative
string is only true at the instant it is painted, which would drag this field
into the liveness problem below for no gain.

**It is the newest message's timestamp in either direction** — the turn's `At`,
or the newest node's — not the session's start and not the last thing the USER
typed: what a reader wants to know is when the conversation last moved, and an
agent working alone for an hour is a conversation that is moving.

**No ticker.** Unlike the alert (§4), this changes only when something arrives,
and everything that makes it change already repaints the bar.

- **`ui.status_verbose` in `config.toml` seeds it**; `^V` overrides for the
  session. Both paths tested — the spec asks for a configurable default and it
  is the kind of thing that ships unwired.
- **`^V` is PASTE in the command box**, by requirement, reversing the
  "deliberately unbound" note in `keymap.go`: that argument was that quoting had
  no use there, and paste is a different verb with an obvious one. Command mode
  only; search keeps the toggle.

## 6. The `!` panel: deferred, on purpose

The spec wants it to become `form show` over the live bound form, and in the
same breath says *"maybe save status for later and just show what we show
today"*. Taken at its word: today's content, moved into the drawer table like
everything else, and it inherits cost and the cache buckets from §1.

## Build order

Each step ships alone; none changes what the pager can do.

1. **`turnState`** (§3) — smallest, and `statusView` needs it.
2. **The drawer table** (§2) — glyphs, names, and `keys()`. It comes BEFORE the
   value that consumes it: `statusView` takes a `drawerID` and renders its
   glyph, so building the value first would mean re-deriving identity inside it,
   which is the exact defect §2 exists to remove.
3. **`statusView` + goldens** (§1) — the bar renders from a value.
4. **`cmdkit.AndMore(n, hint)`** — one spelling of "… 749 more" for the drawer,
   `form show` and the `ls` family, which have three today.
5. **the `picker`** (§4a) — extracted from the completion menu, adopted by the
   queue. It lands before notifications so that notifications is only a row
   renderer and a sink, not a fourth copy of a list.
6. **notifications** (§4) — the only new surface.
7. **`^V`** (§5) — last: the only binding change.

## Decided for you — each reversible with one line

1. **The aria id moves off the rule and into the status row.** Every worked
   example shows a bare rule and `✓ · 123abc · test`; one sketch line says the
   rule right-justifies the id. The examples win. Say the word and it flips
   back.
2. **`𝄞` is the notifications glyph; `♩` is its selection cue.** One symbol
   cannot do both jobs on one screen, and Gluck settled it on 2026-08-28.
   Selection glyphs are per-drawer: queue keeps `♭`.
3. **`cost` and the clock leave the bar** (§1), to the `!` panel.

4. **Search is `⌕` and help is a mode** (§2). The doubled `∴` in the sketch was
   a typo; `?` switches to help mode from everywhere except the command box.
5. **Notifications are transient in the bar** (§4): first left slot,
   `ui.notice_ttl` default 10s, displaced by the next one.
6. **Verbose shows the last interaction** (§5) as `01/02/06 15:04:05`. It
   replaces the session-start clock, which the default bar drops.

*(Nothing is open. The two questions this plan carried — search's identity and
how help stays reachable — were answered by Gluck on 2026-08-28 and are folded
in above.)*
