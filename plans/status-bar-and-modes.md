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

**`keyMode` stays exactly as it is.** Keyboard behaviour and visible identity
are different axes — `help`, `status` and `queue` share `modePanel` while being
three different things on screen — and merging them buys nothing for input mode
or multiplexing.

What changes is smaller: **the drawer is the single owner of presentation**, and
one table keyed by drawer id gives its glyph and name.

| drawer | mode glyph | name | selection glyph |
|---|---|---|---|
| queue | `𝄚` | queue | `♭` |
| notifications | `𝄞` | notifications | `𝄞` *(see open questions)* |
| command | `∴` | command | — |
| search | `∴` | search *(see open questions)* | — |
| help / status / message | `?` `!` — | help / status | — |

`inSearch`/`inJump` stop being consulted by anything but the box that owns them;
the bar and the keymap ask the drawer. Reserved for input mode: one more row.

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
| idle | `𝄐` | `idle 𝄐` | **the catch-all** |

**`disconnected ⠸` is deleted** — a spinner that never turns again, since
nothing repaints after a detach. It folds into `idle`, the declared catch-all.
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
- **`Alert` replaces the status bar's `notice`.** There is no second error
  channel: `setNotice` is deleted, every caller posts a notification, and the
  bar projects the newest unread one until the drawer is opened. That is the
  only reading of "all warnings/errors should be notifications now" that keeps
  trouble where the eye lands.

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

1. **`turnState`** (§3) — smallest, and `statusView` needs it. Deletes
   `disconnected`.
2. **`statusView` + goldens** (§1) — the bar renders from a value.
3. **The drawer table** (§2) — glyphs and names in one place; the bar stops
   re-deriving what is open.
4. **`cmdkit.AndMore(n, hint)`** — one spelling of "… 749 more" for the drawer,
   `form show` and the `ls` family, which have three today.
5. **notifications** (§4) — the only new surface.
6. **`^V`** (§5) — last: the only binding change.

## Open questions

1. **Does the aria id move off the rule into the status row?** Every worked
   example shows a bare rule and `✓ · 123abc · test`; one sketch line says the
   rule right-justifies the id. The examples are taken as authoritative here.
2. **`∴` for both command and search?** One glyph for two modes works if the
   name disambiguates — but the sketch labels the SEARCH row "command", so
   either it is a typo or search has no identity of its own.
3. **`𝄞` is asked to be both the notifications glyph and its selection cue.**
   Two jobs, one symbol, on one screen. Which one gives way?
4. **How does help "always work" when `?` is text?** Inside the command and
   search boxes `?` is a printable character, so the requirement cannot mean
   that key everywhere. Proposed: `:help` in the box, the hint row in every
   mode, `?` everywhere else.
