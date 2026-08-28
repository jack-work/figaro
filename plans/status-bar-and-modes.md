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
  not dangle at a break.
- **Shedding is replaced by wrapping.** The rank ladder exists only because
  everything had to fit on one row. The mantra is the last thing to go.

**Leaving the bar: `cost` and the clock.** The spec says "only input
window/output window by default (plus percentage)". Cost moves to the `!` panel
with the cache buckets it is meaningless without. This is the one subtraction a
reader notices on day one, so it is stated rather than discovered.

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
| *(reserved)* | | input | |

**`⌕` (U+2315), not `🔍`.** The bar is width-sensitive and every other glyph
here is single-width; the emoji magnifier is double-width and font-dependent,
which would make the left group's width a property of the reader's terminal.

**Help is a mode, not a panel.** It sits at the same level as queue, command and
notifications: its own row, its own glyph, and `?` SWITCHES TO IT from every
mode except command, where `?` is text. That settles the "help always works"
question — no `:help`, no Esc-then-`?`, just the key, everywhere it is not
literal.

**One consequence, stated because it is a real cost:** `?` is text in the SEARCH
box too, and this rule takes it. Searching for a literal `?` then needs a route
this plan does not give it. The alternative — exempting search as well as
command — leaves help unreachable from the box where a reader is most likely to
be lost, so the key wins and the literal loses.

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

**This is the one piece with a liveness requirement.** Everything else in the
pager repaints because something arrived — a key, a frame, a resize. An alert
that expires on a clock has to repaint when *nothing happened*, so `statusView`
gains an expiry and the render loop gains one scheduled wake at it. That is
cheap (one timer, armed only while an alert is live, cancelled when it is
displaced) but it must be deliberate: a TTL with no wake is an alert that
disappears the next time you press a key, which reads as randomness.

**Next steps, named:** a notify transport so daemon and agent records arrive
(the aria socket already carries frames; this is one more method); level
filtering in the drawer; an unread count in the bar (cut here because its
reset semantics are undefined, and "do something simple for now"); an
alternative sink; nvim-style levels with timeouts.

## 5. Verbose (`^V`), and the paste it must not eat

`^O` (verbose tool output) is a different axis and stays. `^V` toggles **the
bar's verbosity**: state names, `system.model`, and the hint row.

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
5. **notifications** (§4) — the only new surface.
6. **`^V`** (§5) — last: the only binding change.

## Decided for you — each reversible with one line

1. **The aria id moves off the rule and into the status row.** Every worked
   example shows a bare rule and `✓ · 123abc · test`; one sketch line says the
   rule right-justifies the id. The examples win. Say the word and it flips
   back.
2. **`𝄞` is both the notifications glyph and its selection cue.** The spec asks
   for a treble clef in both places and nothing says they must differ; an
   aesthetic objection is not an ambiguity.
3. **`cost` and the clock leave the bar** (§1), to the `!` panel.

4. **Search is `⌕` and help is a mode** (§2). The doubled `∴` in the sketch was
   a typo; `?` switches to help mode from everywhere except the command box.
5. **Notifications are transient in the bar** (§4): first left slot,
   `ui.notice_ttl` default 10s, displaced by the next one.

*(Nothing is open. The two questions this plan carried — search's identity and
how help stays reachable — were answered by Gluck on 2026-08-28 and are folded
in above.)*
