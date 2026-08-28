# The status bar, the modes, and the drawer vocabulary

> **STATUS: PLAN.** Written 2026-08-28 against
> `notes/figaro/ui/status-bar-expected.md`. UI only: nothing here changes the
> wire, the store or the provider layer. It is the tidying that
> [transcript-composer.md](transcript-composer.md) (input mode) and transcript
> **multiplexing** both need first, and it is worth doing on its own terms
> because the bottom two rows of the pager are the only thing on screen that is
> always true.

## The three things wrong today

1. **The status row is assembled from a session, not from a value.** `statusLine`
   reads a mutex, formats, sheds tokens by rank and returns a string, all in one
   function against one `*sessionStatus`. There is exactly one of those per
   process. **Multiplexing needs N**, one per pane, and a pane's bar must be
   renderable without owning the session that produced it.
2. **A mode is a set of booleans that four files check.** `t.inSearch`,
   `t.inJump`, `t.drawer.kind`, `t.drawer.name` — `mode()` derives a `keyMode`
   from them for the keymap, and every other consumer re-derives its own answer.
   Nothing owns "what mode am I in, what is it called, and what does it look
   like". Input mode is a fifth boolean unless that is fixed first.
3. **The state vocabulary is prose.** `turnLabel()` returns `"completed ✓"`,
   `"interrupted !"`, `"disconnected ⠸"` — symbol and name fused into a string,
   no colour, and no way to ask for one without the other. The requirement is
   symbols by default and names under a toggle, which that shape cannot express.

Everything below follows from fixing those three, in that order.

## 1. `statusView`: a value, and a pure renderer

```go
// What the bar SAYS. Snapshotted under the session's lock, rendered outside it.
type statusView struct {
    Mode    modeID        // which mode owns the keyboard (§2); modeTranscript is "none"
    State   turnState     // §3
    Aria    string        // "123abc"
    Mantra  string
    Ctx     ctxUsage      // used, limit, exact
    Notice  string        // the one token that never sheds
    Verbose bool
}

func (v statusView) render(w int) []string   // 1 row, or 2 when w is small
```

**Pure, and therefore golden-testable at every width**, which the current
renderer is not. One test table, one file of goldens: 40/60/80/120 columns ×
{plain, drawer open, notice, verbose, no mantra}. Today a width regression is
only visible in a pty.

The layout, from the requirements:

```
── the transcript rule, unchanged ───────────────── aria 3b7aff0a · 1–29/118 ──
𝄚 queue · ✓ · 123abc · test                              9.8k/1.0m (1.0%)
└ mode ──┘ └state┘ └aria┘ └mantra┘                        └─ right-justified ─┘
```

- **Left**, in order: the mode token (icon + name; absent in plain transcript
  mode), the state symbol, the aria id, the mantra. `·` between.
- **Right**, right-justified: context `used/limit (pct)` and nothing else by
  default.
- **Narrow**: left-justify both and put the right group on its own row, with a
  blank row between — three rows, not a truncation.
- **Verbose**: wrap both sides across rows, truncate each FIELD (not the line),
  and **a wrapped row does not carry the `·` separator** — the separator joins
  fields on a row, it does not dangle at a break.

**Shedding is replaced by wrapping.** The rank ladder (mantra first, then cost,
then ctx, then time) exists because everything had to fit on one row. With a
second row available for narrow panes, the only thing that still sheds is the
mantra, and only when even the wrapped form will not fit.

**Dropped from the bar, deliberately:** `cost` and the start `time`. The
requirement says "only input window/output window by default (plus percentage)".
Cost belongs in the `!` panel with the cache buckets it is meaningless without;
the clock belongs nowhere (every terminal already has one). *This is the one
change a reader will notice on day one, so it is called out here rather than
discovered.*

## 2. `modeID`: one table, the way the keymap is one table

`keyMode` already enumerates the modes the KEYBOARD cares about. Promote it: one
row per mode, carrying everything a mode decides.

```go
type modeRow struct {
    id      modeID
    icon    string      // "𝄚"  the drawer/mode glyph
    name    string      // "queue"
    keys    keyModeSet  // what keymap.go already calls a mode
    drawer  drawerKind  // what the region below the rule holds
}
```

| mode | icon | name | notes |
|---|---|---|---|
| transcript | — | — | no token: the bar shows state · aria · mantra |
| queue | `𝄚` | queue | `Q` |
| notifications | `𝄞` | notifications | `N`, §4 |
| command | `∴` | command | `:` |
| search | `∴` | search | `/` — the requirements' sketch labels this row "command"; **taken as a typo**, see the open questions |
| help | `?` | help | |
| status | `!` | status | |
| *input* | *(reserved)* | *(reserved)* | the next piece of work slots in here |

**Selection is a separate glyph from the mode.** `♭` marks the selected row
INSIDE a drawer; the mode icon sits in the status bar. Two different questions —
"which drawer is open" and "which row is under the cursor" — got one symbol in
the sketch and need two.

This kills `t.inSearch` and `t.inJump` as public state: they become
`t.mode == modeSearch`. The keymap's `mode()` reads the field instead of
deriving it, which is also the bug-shaped part: today `mode()` and the drawer
can disagree for one frame.

## 3. `turnState`: symbol, name, colour

```go
type turnState uint8   // stateThinking, stateDone, stateHup, stateError, stateIdle

func (s turnState) symbol(tick int) string  // thinking animates; the rest are static
func (s turnState) name() string            // "" for thinking: it has no name
func (s turnState) style() term.Style       // hup: gray. error: red. rest: default
```

| state | succinct | verbose | when |
|---|---|---|---|
| thinking | animated | *(no name, by requirement)* | a turn is in flight |
| done | `✓` | `done ✓` | turn complete, no hup |
| hup | `!` | `hup !` (gray) | user hangup |
| error | `✗` | `error ✗` (red) | hup due to error |
| idle | `𝄐` | `idle 𝄐` | **the catch-all**: none of the above |

**`disconnected ⠸` is deleted.** It is today's fifth state and it is a lie
wearing a spinner: nothing repaints after a detach. It folds into `idle`, the
declared catch-all.

**Hup is a state, not an error** — the requirement says so explicitly, and it is
why `hup` is gray rather than red. A user who stopped a turn on purpose has not
had something go wrong.

## 4. Notifications: a sink, a drawer, and nothing clever

The requirement is explicit that this should be **simple now, with next steps
called out**. So:

- **A process-wide ring** (`logring` already exists) fed by a `slog` handler, so
  `slog.Warn`/`Error` from anywhere — including the daemon client paths that
  currently vanish — become notifications. Bounded, newest-last, no persistence.
- **`N` opens the drawer**; `^N`/`^P` move; `y` yanks the selected line; **no
  delete**, because a log you can edit is not a log.
- **Severity is a row glyph**, not a colour alone: `♪` on the selected row, the
  severity word (`error`/`warning`) leading the text.
- **The status bar shows a count** when unread notifications exist, in the mode
  token's place when no drawer is open: `𝄞 3`. This is the only affordance that
  says "something happened while you were reading".

**Next steps, not now:** filtering by level in the drawer, an alternative sink
(file/socket) for a real session, persistence across restarts, and nvim-style
`vim.notify` levels with timeouts. The ring and the drawer are the substrate for
all four; none of them changes the drawer's contract.

## 5. The `… and N more` pattern, one place

Three spellings exist today (`… %d more`, `… %d more (-a for all, -n N for N)`,
and the drawer's own). One helper in `cmdkit`, used by the drawer, `form show`
and the `ls` family:

```go
func AndMore(n int, hint string) string   // "… 749 more" / "… 749 more (-a for all)"
```

Trivial, and it is the kind of thing that stays inconsistent forever if it is
not done while the drawer is already open on the bench.

## 6. Verbose (`^V`), and the paste it must not eat

Verbose today is `^O` (verbose TOOL output) — a different axis, and it stays.
The new `^V` toggles **the bar's own verbosity** and nothing else:

- state names beside their symbols
- `system.model` from the bound form
- the hint row (`? help`, and the mode's own keys)

**`^V` IS PASTE INSIDE THE COMMAND BOX**, by requirement, which reverses the
"deliberately unbound" note in `keymap.go`: the box's argument was that quoting
had no use there, and paste is a different verb with an obvious one. So: `^V` in
`modeCommand`/`modeSearch` inserts the clipboard; everywhere else it toggles
verbosity. The keymap already expresses exactly this (a chord bound per mode).

## 7. The `!` status panel: deferred, on purpose

The requirement wants it to become `form show` filtered to system properties,
over the LIVE bound form — and says in the same breath *"maybe save status for
later and just show what we show today"*. Taken at its word: **the panel keeps
today's content**, moves into the mode table like every other drawer, and the
form-backed version waits for the live-form work it actually depends on. Cost
and the cache buckets move here, since they leave the bar (§1).

## Build order

Each step is shippable alone and none of them changes what the pager can do:

1. **`turnState`** (§3) — smallest, and `statusView` needs it. Deletes
   `disconnected`.
2. **`statusView` + golden tests** (§1) — the bar renders from a value.
3. **`modeID` table** (§2) — deletes `inSearch`/`inJump`; the bar gains its
   mode token; the drawer gains its icon.
4. **`AndMore`** (§5) — while the drawer is open.
5. **notifications** (§4) — the only new surface.
6. **`^V`** (§6) — last, because it is the only one that changes a binding.

## Open questions for review

1. **Does the aria id move off the rule and into the status row?** Every worked
   example in the requirements shows a bare rule and `✓ · 123abc · test`, but
   one sketch line says the rule right-justifies the figaro id. The examples are
   taken as authoritative here (the id in the bar, the rule keeping only the
   scroll position) — but it is a real fork.
2. **`∴` for both command and search?** The sketch gives search the `∴` icon and
   the word "command". One glyph for two modes is fine if the NAME
   disambiguates; if not, search wants its own.
3. **Is `9.8k/1.0m (1.0%)` context used/limit?** The arithmetic says yes
   (9.8k/1.0m ≈ 0.98%). "input window/output window" then means the context
   window, not tokens in/out. Confirmed by numbers, but say so.
4. **Does dropping `cost` from the bar bother anyone?** It is the one visible
   subtraction.
