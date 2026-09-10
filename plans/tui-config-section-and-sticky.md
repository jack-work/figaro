# The TUI's own config section, and a sticky header that appears on its own

Commission of 2026-09-09, second note. Same worktree (`queueform`).

> could we make the sticky header appear automatically according to config?
> make it similar to other relevant tui config if possible, and ideally
> separate the tui config in a section from the angelus config.

---

## 1. What I found, which changes the shape of the answer

`[cli]` already exists and its doc comment states the very principle asked
for (`internal/config/config.go:58`): *"They live in their own [cli] section
because everything else here is the SERVER's state."* Two things are wrong
with it.

### 1.1 Five of the eight client settings are dead

Every `CLIConfig` field has a `Loaded` accessor. Counting non-test callers
outside `internal/config`:

| accessor | callers | verdict |
|---|---|---|
| `CoordFormat()` | 5 | wired |
| `StatusLine()` | 1 (`runsession.go:106`) | wired |
| `StreamEmitIntervalMs()` | 1 (`internal/figaro/turn.go:1439`) | wired — **to the daemon** |
| `StatusVerbose()` | **0** | dead |
| `NoticeTTL()` | **0** | dead |
| `EchoPrompt()` | **0** | dead |
| `StreamCPS()` | **0** | dead |
| `StreamFirstByteBypassMs()` | **0** | dead |

`session_states.go:135-143` documents this exact bug class in the first
person — *"setNoticeTTL existed, config.NoticeTTL existed, and nothing called
either… A default that has to be installed by a caller is not a default"* —
and fixed it by moving the default into the constructor while **leaving the
config key unread.** The lesson was written down and not generalized.

So adding `sticky` to the config as-is would make a sixth dead key. The ask is
therefore two changes, and the first is the one that matters.

### 1.2 A daemon setting is living in the client's section

`stream_emit_interval_ms` is read at `internal/figaro/turn.go:1439` — by the
**agent**, to size its emit throttle — out of a section whose doc says
"Nothing here reaches the daemon." That comment is false today, and it is
exactly the confusion the note asks to be cleared.

### 1.3 Two of the dead settings have no consumer left to wire

`stream_cps` and `stream_first_byte_bypass_ms` describe a **pacer**. There is
no pacer in the tree (grep finds none; `cli.go:457`'s help text still promises
one). They are not unwired features; they are settings for a component that
was removed. **Delete them**, do not wire them.

---

## 2. The split

```toml
[cli]                              # the client PROCESS: true before a screen exists
echo_prompt            = true
interactive            = true
check_updates          = true
update_check_ttl_hours = 24
ref_sigil              = "@"

[cli.tui]                          # the PAGER: only meaningful on a screen
sticky        = "auto"
status_line   = true
status_verbose = false
notice_ttl    = 10
coord_format  = "02/01/06 15:04:05"

[stream]                           # THE DAEMON'S. Out of [cli] entirely.
emit_interval_ms = 90
```

The line is not "client vs server" (that is `[cli]` vs the rest, and it is
already drawn). It is **"does this only mean anything once something is
painting"**. `echo_prompt` and `ref_sigil` govern a non-interactive
`figaro send` too; `sticky` and `coord_format` cannot exist without a pager.

`[cli.tui]` rather than a top-level `[tui]` because the TUI *is* the client —
a top-level section would suggest a third party alongside the client and the
daemon.

---

## 3. The seam, so that a setting cannot be dead

One function, and only one, turns config into the pager's settings:

```go
// internal/cli
func (l *config.Loaded) RenderSettings() renderSettings
```

All five `renderSettings{…}` literals — `aria.go:155`, `cli.go:388`,
`fork.go:275`, `listen.go:104`, `send.go:479` — become

```go
set := renderSettingsFrom(loaded)
set.verbose, set.listen, set.record = opts.verbose, opts.listen, opts.record
```

i.e. **config first, per-invocation flags as overrides on top.** Today it is
the reverse: each site hand-lists the fields it happens to care about, which
is precisely how `coordFormat` got wired at all five and `sticky` at none.

**The test that keeps it true.** Reflect over the `tuiConfig` struct; for each
field, set a non-default value, build the settings twice (default vs set), and
demand something observably differ. A field nobody reads fails. That is the
generalization `session_status.go` stopped one step short of, and it is what
makes "add a key" a safe operation forever after.

While in there: wire `status_verbose` (`sessionStatus.setVerbose` exists,
`session_status.go:500`) and `notice_ttl` (`setNoticeTTL` exists, `:198`).
They are one call each. `echo_prompt` needs a look — find its intended site or
delete it too.

---

## 4. Sticky: three values, not two

```
sticky = "auto"   (default)  |  "always"  |  "never"
```

- **`never`** — today's behaviour before `s` is pressed.
- **`always`** — on whenever the reader is inside a turn's answer.
- **`auto`** — `always`, gated on room.

`auto` earns its existence from the header's own design
(`transcript_sticky.go:11-20`): it **floats** — it covers the rows it stands
on rather than pushing them down. On a 12-row pane a 3-row header therefore
hides a quarter of the conversation with no warning. Gate it the way the pager
already gates its other growable region: `queuedRowsMax` is floored by `h/3`
(`livelog_bridge.go:997`), so `auto` = on iff `headRows() <= h/6` (or a small
absolute floor). Reusing the idiom beats inventing a rule.

`s` still toggles for the session. Config decides what it starts as — the
phrasing `status_verbose` already uses: *"^V toggles it for the session; this
decides what it starts as."* So the note's ask, exactly stated, is: **make
`sticky` obey the rule `status_verbose` already documents, and then wire both.**

**One subtlety.** `transcript.sticky()` (`transcript_sticky.go:29`) reads
`view.settings.sticky`, a bool. `auto` is a *decision*, not a state, and the
height is not known where the settings are built. So:

- `renderSettings.stickyMode` holds `never|always|auto` from config;
- `renderSettings.stickyOverride *bool` holds what `s` said, if anything;
- `transcript.sticky()` resolves: override if set, else mode, with `auto`
  consulting the current height.

That keeps `s` authoritative (a reader who asks for the header on a short pane
gets it) and keeps `auto` honest (it re-decides on resize, for free, because
it is computed per frame).

---

## 5. Strictness instead of a second migration ladder

`Load` (`config.go:866`) carries a hand-written per-key ladder folding
pre-`[cli]` top-level keys, written because — its comment — *"an unread key
here is silent: measured, `echo_prompt = false` at the top level yielded
`EchoPrompt() == true` with no error and no warning."* Adding `[cli.tui]`
invites a second such ladder. Don't grow it. Cure the silence instead:

- BurntSushi's `toml.Decode` returns `MetaData`; **`md.Undecoded()` is the
  list of keys nothing consumed.** Warn once at startup, naming each key and
  the file. A typo, a relocated setting, or a key from a future version then
  says so out loud.
- Keep the existing top-level ladder and extend it to `[cli] → [cli.tui]`, but
  now *with* the warning: the old spelling is honoured and the user is told
  where it moved.

A config.toml is **user-authored prose, not serialized agent state.** It earns
a migration and a loud warning; it does not earn silent invalidation, and it
does not earn a hand-maintained ladder per rename either.

---

## 6. Staging

1. `md.Undecoded()` warning in `Load`. Two lines, immediately useful, and it
   is the thing that would have caught all five dead keys years ago.
2. Delete `stream_cps` / `stream_first_byte_bypass_ms` (no consumer exists).
   Fix `cli.go:457`'s help text, which still promises a pacer.
3. Move `stream_emit_interval_ms` → `[stream] emit_interval_ms`, with the
   compat read and warning. Daemon-side, one call site.
4. Introduce `[cli.tui]` + `tuiConfig`, `renderSettingsFrom(loaded)`, and the
   reflective "no field goes unread" test. Convert the five literals. Wire
   `status_verbose` and `notice_ttl` in passing.
5. `sticky` as a three-valued mode plus the `s` override, and `auto`'s height
   gate. Painting change ⇒ tmux, real pty, per the `tmux-testing` skill: a
   header that appears at one height and not another is exactly the class of
   claim a unit test will confidently get wrong.
6. Document `[cli.tui]` wherever `[cli]` is documented (it appears to be
   nowhere but `plans/status-bar-and-modes.md` — worth a real reference page).

---

## 7. Open questions

1. `[cli.tui]` or top-level `[tui]`? I lean `[cli.tui]`, §2.
2. Ship `sticky = "auto"` as the default, or `"never"` for one release so no
   pager grows a header unasked? I lean `auto` — that is the note's whole
   point — but it is a visible default change.
3. `echo_prompt`: wire it or delete it? Needs one look at what it was for.
4. Should the reflective completeness test extend to **all** of `CLIConfig`,
   not just `tuiConfig`? It would have caught every one of the five. The
   awkwardness is that some keys are read by the daemon and some by the
   client, so "observably read" needs two harnesses. Worth it.
