# 2026-09-16: remappable keys, a client-owned config file, and a help pit that tells the truth

**Status:** brainstorm. Nothing built, no product code touched. Scribe: aria
`a57d46f7`, working `duck/keymap-config`. Commissioned by Gluck tonight:

> fork another aria to holistically investigate the possibility of separating
> the tui configuration and allowing users to remap any keybindings they wish,
> such that they would be rendered in the help pit.

Three questions that are one: the keymap is a table, the help pit is a
hand-written second copy of it, and there is no file for a user to write. Every
claim names a file and a line; where I guess, I say so.

## 1. What is true today

**The table.** `internal/cli/keymap.go:154` declares 171 rows. A row is a
`chord`, a `keyModeSet` bitmask, an `openPolicy` with a `why`, a `helpID`, and
exactly one of a `pagerFunc` or an `inputFunc` (`keymap.go:118-134`). Seven
modes (`keymap.go:13-23`). `init()` compiles the rows into flat arrays
(`keymap.go:859-861`); dispatch is one array load per keystroke
(`keymap.go:935-977`).

**Two levels, and the upper one wins silently.** The read loop tries
`inputAct.input` first and hands the key to the pager only if nothing owns it
(`internal/cli/stream.go:817-841`); `transcript.dispatch` is the pager half
(`internal/cli/transcript.go:1749-1870`).

**The table is read by the DECODER, not only by dispatch.** A user row does not
only change what a key does; it changes what the terminal's bytes are taken to
be:

- `ctrlChordBoundIn` (`keymap.go:1037`) decides, per mode, whether a CSI-u
  Ctrl+letter report stays a chord or collapses to its control byte
  (`stream.go:722`).
- `metaBoundIn` (`keymap.go:1065`) decides whether `ESC x` is Alt+x or two keys
  (`stream.go:735`, `stream.go:767`, `key_input.go:203`). Its own comment:
  in a mode that has Meta rows, `ESC <byte>` is a Meta chord whether or not
  that chord is bound.
- `opensTranscript` (`keymap.go:1084`) decides whether a key pressed during
  inline streaming yanks the pager up (`stream.go:809`).

**Three inventories, two of them wrong.** The help panel is a second hand-kept
definition (`helpRows`, `keymap.go:737-782`), and a prose inventory comment is a
third (`keymap.go:139-152`). Both drift:

- `keymap.go:143` lists `o` among taken plain letters. No `byteChord('o')` row
  exists anywhere; verbosity is `metaChord('m')` at `keymap.go:204`. This is
  the astra review's finding 13.
- `keymap.go:145-146` lists `b c h l w` and `L M` as free. Visual mode binds
  every one of them: `keymap.go:421,422,437,438,444,445`.
- A fourth copy ships in the skill: `ui-stream.md:383-394,452-474`, with
  `skills/figaro/cli.md:49,294` still assigning verbosity to Ctrl-O.

The anti-drift test checks only the SET relation, not the key column:
`TestHelp_EveryVisibleBindingIsDocumented` (`internal/cli/keymap_test.go:408-431`)
demands that every visible binding point at a row and every row document some
binding, so a row may print any string it likes and pass. The frozen-strings
test (`keymap_test.go:351-403`) then locks the wrong string in.

**The printer exists, in the test binary.** `chord.String()` is defined at
`keymap_test.go:18-34` and `navName` at `:37`. Production has no way to spell a
chord, and no way to parse one.

**Config.** One file, `~/.config/figaro/config.toml`, one struct
(`internal/config/config.go:37-77`). `[cli]` holds the client's settings and
says so: "everything else here is the SERVER's state" (`config.go:58-61`),
"Nothing here reaches the daemon" (`config.go:109`). The client loads it once,
at `internal/cli/cli.go:91` through `mustLoadConfig` (`internal/cli/config.go:24`);
there is no watcher and `go.mod` has no fsnotify. `pagerSettings`
(`internal/cli/nodes.go:520-537`) is the one seam from config to the pager.

**The daemon's update path, and the hole beside it.** A client patches the
server's config over `angelus.configure`: `ConfigureRequest` carries
`default_outfit`, an outfit body and `refresh` (`api/rpc/admin.go:48-66`), and
the handler writes, re-reads, invalidates (`internal/angelus/configure.go:49-105`).
`SetDefaultOutfit` states the doctrine: "a client may not write the daemon's
config itself" (`config.go:1049-1051`). There is no `figaro config` verb: the
registered verbs are `cli.go:148-1594` and none writes `[cli]`. The attendance
duck doc found the same hole tonight ("Config has a client section but no
update path").

## 2. Separating the TUI configuration

**Verdict: a separate file, `keys.toml`, owned by the client, and NOT a second
TOML section.** Reasons, in order of weight:

1. **The arbitration disappears.** `config.toml` is the daemon's file by the
   rule at `config.go:1049`. A keymap in it means a client asking a daemon to
   write a table the daemon must never read. A file the client writes directly
   has no RPC, no refresh, no ordering question.
2. **The shape is different.** Everything in `[cli]` is a scalar with a
   default; a keymap is a list of rows with merge semantics (replace, unbind,
   refuse), sharing no loader, validator or error report with the scalars.
3. **It is the direction of travel.** `plans/tui-config-section-and-sticky.md`
   §2 proposes `[cli.tui]` for pager-only scalars; `keys.toml` is the same cut
   one level further, and scalars stay in `config.toml` under `[cli.tui]`.

```toml
# ~/.config/figaro/keys.toml
[[key]]
chord  = "^N"          # both encodings of one physical key (§3.1)
mode   = "transcript"  # the default when omitted
action = "select-next"

[[key]]
chord = "M-m"
mode  = "all"
action = "none"        # unbind
```

**What owns it.** A small package, `internal/keyconf` (name is mine), parses
`keys.toml` into plain data with no dependency on `internal/cli`, which applies
the rows because only it knows the registry and the refusal rules. The parser is
then testable without a pager and `internal/config` keeps one concern.

**How it loads.** At session start, explicitly, on the session path (near
`pagerSettings`, `nodes.go:528`), never from `init()`: loading at init would make
the package table depend on the developer's own `~/.config` and silently rewrite
what every keymap test measures (§5).

**When it changes mid-session.** Ship the first two of three:

- *Start-only* (default): `keys.toml` is read when the process starts.
- *Explicit reload*, `:reload-keys` in the command box. Rebuilding is one pass
  over 171 rows (`buildKeyIndex`, `keymap.go:861`), so cost is not the issue.
  **Correctness is:** the compiled arrays are package globals read without a
  lock by the read loop (`stream.go:817`) and the render loop
  (`transcript.go:1755`). A reload swaps an immutable value behind an
  `atomic.Pointer` rather than mutating arrays. §5 wants that anyway.
- *Watcher*: rejected. A new runtime dependency (invariant 11) to buy a reload
  the user can ask for in one keystroke.

**The CLI update path, congruent with the daemon's** patch, write, re-read,
invalidate (`configure.go:83-104`), with no daemon in it:

```
figaro keys                       # print the live map (the help pit's rows)
figaro keys bind <chord> <action> [--mode transcript]
figaro keys unbind <chord> [--mode …]
figaro keys reset [<chord>]
figaro doctor keys                # parse, report refusals, exit nonzero on any
```

Each write goes through one function that rewrites `keys.toml` preserving the
rest, as `SetDefaultOutfit` does for `config.toml` (`config.go:1049-1073`). What
it cannot do is reach a pager in another process: our only broadcast is the
daemon's notification channel, and a client file behind it makes the daemon the
writer of a config it may not read. For live cross-process propagation the
honest mechanism is a form: question 3.

## 3. Remappable bindings

### 3.1 How a user names a chord

**One spelling, both directions.** Promote `chord.String()` (`keymap_test.go:18`)
to production as `spellChord`, write `parseChord` as its inverse, round-trip both
over all 171 rows. It is the spelling the help pit prints, so the config language
and the panel cannot diverge:

| kind | spelled | examples |
|---|---|---|
| printable byte | itself | `j` `:` `?` `$` |
| control byte | caret, or a name | `^D` `^L` `Enter` `Esc` `Tab` `Backspace` `Space` |
| meta | `M-` prefix | `M-m` `M-<` `M-DEL` `M-Enter` |
| nav | a name | `Up` `Down` `PgUp` `PgDn` `Home` `End` `Left` `Right` |

**The CSI-u problem.** One physical key has two encodings and the table binds
them as separate rows: `byteChord(0x0e)` (`keymap.go:397`) is pager-level
select-next, `ctrlChord('n')` (`keymap.go:257`) is input-level select-and-extend
and reads `ev.shift`. A user cannot be asked which one their terminal sends. So:
**the user writes `^N`; the loader binds both encodings; the action decides the
level.** The byte form cannot carry Shift, a fact about terminals rather than
about our table, which `figaro keys` should footnote.

**Three chords stay welded**, as `keymap.go:150-153` says: Ctrl+M is Enter
(0x0d), Tab is ^I (0x09), Ctrl+[ is Esc. `parseChord` rejects `^M`, `^I`, `^[`
naming the key they actually are.

**Binding a Meta chord changes decoding in that mode.** Adding `M-x` to
transcript mode makes `Esc x` there a swallowed Meta chord rather than two keys
(`metaBoundIn`, `keymap.go:1065`); removing a mode's last Meta row gives Esc
back. `figaro doctor keys` should say so when a file adds the first Meta row to
a mode that had none.

### 3.2 How modes are named

Mode names exist today only in the test binary (`modeName`, `keymap_test.go:54`).
Making them user-facing freezes seven strings: `incipit`, `transcript`,
`search`, `command` (the `:` box, internally `modeJump`), `pit` (internally
`modePanel`), `visual`, `fork`, plus aliases `pager` for the six modes with the
pager up and `all` for `inAnyBox`. The `&^ inJumpBox` set algebra
(`keymap.go:166` and following) stays internal: a user names modes, not set
differences. Omitting `--mode` means `transcript`, where a mistake is cheap.
Two of the seven rename an internal identifier and both are worth it: `jump` has
been the command line since `:listen`/`:attend`/`:send` shipped, and the help pit
already says `(in :)`.

### 3.3 Action names become a public vocabulary

They must. A user binding a key names a verb, and today the verbs are 103
distinct `pager:` functions and 15 `input:` functions.

**The single decision that limits the cost: user-facing names are not Go
identifiers.** A hand-written registry maps kebab-case names to funcs, each
entry carrying the level and a one-line description:

```go
{"scroll-down", levelPager, pagerLineDown, "scroll one line"},
```

A Go rename then touches one line of the registry and no user's file. Renaming
a *user-facing* name is a breaking change with no alias to soften it:
`agents.md` forbids back-compat shims in dev mode, and a keys file that silently
half-applies is the failure mode `config.go:993-999` measured. The test that
keeps the registry complete is one the repo already runs for this question:
compare function pointers with `reflect.ValueOf(a).Pointer()`, as
`TestKeymap_ActionArraysAgreeWithTheIndex` does (`keymap_test.go:478`). Every
row's func appears in the registry; every entry is reachable.

### 3.4 Collisions, shadowing, and bad input

**Merge rule.** A user row is keyed by (mode, chord, level) and REPLACES the
built-in row at that key. `action = "none"` unbinds. Two user rows with the same
key is a file error, not last-one-wins. That differs from the built-in table,
where any duplicate is fatal (`TestKeymap_NoDuplicateBindingPerMode`,
`keymap_test.go:128-152`), and that test keeps describing the defaults.

**Cross-level shadowing is the dangerous case.** Input rows run first
(`stream.go:817`), so binding an input-level action onto `j` in transcript mode
kills pager `j` with no message. The loader detects a user row shadowing a live
row at the other level and refuses it, naming both.

**Modes that own the keyboard behave differently, and the loader must encode
which:**

- `search` and `command`: a key with no row is LITERAL TEXT
  (`transcript.go:1758,1777`), so a printable-byte row steals a character from
  typing. Refuse printable bytes in these two modes outright.
- `visual`: a key with no row is inert (`transcript.go:1791-1799`). Adding is
  safe; removing makes the key dead rather than falling through.
- `pit`: a key with no row wipes the panel and then acts
  (`transcript.go:1800-1810`), so a new row removes a dismissal gesture.
- `fork`: a key with no row abandons the half-typed gesture and acts normally
  (`stream.go:818-827`, `transcript.go:1781-1790`); a new row shrinks that
  escape.

**Unbindable, and why.** The loader refuses, by name, with the reason:

| chord | mode | reason |
|---|---|---|
| `^C` | everywhere it is bound today (`keymap.go:166-169`) | the process's guaranteed exit; also `cmdAbort` in the command box (`keymap.go:596`) |
| `Enter` | `search`, `command` (`keymap.go:483,488,513,518`) | a box you cannot submit |
| `Esc` | `search`, `command` (`keymap.go:493,523`) | a box you cannot leave, and it is the escape-sequence prefix besides |
| `^M` `^I` `^[` as spellings | any | they are Enter, Tab, Esc (`keymap.go:150-153`) |
| printable bytes | `search`, `command` | they are text |

`^D` is deliberately NOT on this list: it is detach outside the box and
delete-forward inside it (`keymap.go:171-175` and `keymap.go:562`), and a user
who wants it elsewhere is asking for something coherent.

**Bad input.** An unknown action, an unparseable chord, an unknown mode or a
refused row: keep the built-in row, warn once on stderr naming file and line,
and carry the diagnostics into the session so the help pit can draw a final row,
`keys.toml: 2 rows ignored (figaro doctor keys)`. Failing the whole load leaves a
reader with a pager a typo will not open; failing silently is the defect
`config.go:993-999` measured. `figaro doctor keys` exits nonzero on any of them;
the doctor verb already hosts this class of check (`cli.go:1468`).

## 4. The help pit renders the result

A hand-written key column is not merely stale under remapping, it is false for
anyone who edited the file. The derivation:

1. `helpRow` keeps `id` and `text`, and loses `keys` (`keymap.go:731-736`).
2. The key column is computed: gather every visible live row with that `helpID`,
   spell each chord, join in table order, and take the mode qualifier
   (`(in :)`, `(in v)`) from the `modes` bitmask rather than from prose.
3. A row whose bindings are all gone is omitted rather than drawn with an empty
   column, which subsumes half of `TestHelp_EveryVisibleBindingIsDocumented`.

**What derivation costs, honestly.** The current column is better prose than a
join: `j/k · u/d · gg/G` versus `j · k · u · d · g · G`. And `gg` cannot be
derived at all, because the doubled `g` is state (`t.pendG`,
`transcript.go:1796`), not a row. So: allow an optional prose override, and make
it verifiable rather than decorative. The override is used only when the set of
chords it mentions equals the derived set, so the moment a user remaps anything
under that id it is discarded and the mechanical join is printed. One test,
running `parseChord` over the override's tokens against the derived set, keeps
every override honest. The frozen-strings test (`keymap_test.go:351`) then
measures the default map, which is what it is for.

**The end state worth naming.** Once actions carry a description, `helpRows`
disappears and the help pit becomes a projection of the registry. One
definition, with the user's file a patch on the chord side of it. Not in the
first pass, but every step above should be compatible with it.

**The fourth copy.** `skills/figaro/reference/ui-stream.md:383-394,452-474`
should point at `figaro keys` instead of holding a table that cannot be right
for a user who remapped; at minimum, one line above each table saying these are
the defaults. The stale Ctrl-O rows in `skills/figaro/cli.md:49,294` are wrong
today, independent of all this.

## 5. What this does to the frozen oracle

`keymap_equiv_test.go:13-33` calls itself "a frozen oracle, not an aspiration",
generated from pre-refactor code (45bee38). It sweeps every byte 0x00-0x7f and
every nav key per state (`sweepPager`, `:1013-1030`), verifies at `:1035`, and
`TestKeymap_OracleCoversEveryPagerRow` (`:1063`) demands every pager row be live
in a state it starts in. `keymap_input_equiv_test.go` is the input-level twin;
`keymap_regen_test.go:11-21` regenerates both under `ORACLE_REGEN=1`.

Remapping does not weaken it, if the layering is right:

- **The oracle must measure the DEFAULT map.** It reads the globals `init()`
  built (`keymap.go:859`). Applying user config at init would fail the oracle on
  any developer who has a `keys.toml`, for reasons unrelated to the code. User
  rows are applied by an explicit call on the session path.
- **Better: make the compiled set a value.** `keymapSet{rows, pagerAct,
  inputAct, pagerIndex, inputIndex, openers, metaBound}` from a pure function,
  with an `atomic.Pointer[keymapSet]` for the live one. The oracle then builds
  the default set explicitly, and a reload swaps a pointer instead of racing two
  loops (§2). Cost: the 31 non-test references to the globals (`keymap.go` 20,
  `transcript.go` 9, `stream.go` 2) become field accesses.
- **One new test, and it is the one that matters:** an empty user layer yields a
  set identical to the defaults, and any layer yields one that still satisfies
  `EveryRowIsWellFormed`, `IndexAgreesWithTheTable`, `NoDuplicateBindingPerMode`
  and `ActionArraysAgreeWithTheIndex`. Those four become functions over a set,
  run on the defaults and on a fixture layer; all four already walk the table
  generically.

The oracle never could say anything about a user's session. It says the shipped
defaults behave as they did before the table refactor, and that survives.

## 6. Where this lands relative to prior art

- `plans/tui-config-section-and-sticky.md` is the direct ancestor: its
  `[cli.tui]` split, one-seam rule (shipped, `nodes.go:520-537`),
  `md.Undecoded()` warning and staging all stand. `keys.toml` is the next cut
  after `[cli.tui]`, not a rival to it.
- `plans/transcript-command-mode.md` makes the `:` box the CLI's grammar, so
  `figaro keys bind …` works from `:` free through `runThroughRouter`. An
  argument for a real verb over a pager-only gesture.
- `plans/status-bar-and-modes.md` gave us "modes proper, not flags checked ad
  hoc"; its `statusView`-as-a-value argument is §5's.
- `duck/2026-09-16-attendance-form.md` hit the same missing update path from the
  other end and proposes an in-memory form for client state, which is the
  live-propagation mechanism I decline for v1 (question 3).
- The review's finding 13 asked for a derived inventory; §4 is that, made
  mandatory by remapping. Finding 11 is why §4 ends with the skill tree.

## 7. Two shapes, and what each costs

**Shape A, the cheapest useful version, in a day.** No config file, no remapping.

1. `spellChord` + `parseChord` in production, round-tripped over all 171 rows.
2. Help key column derived, with the verified prose override (§4).
3. `figaro keys` prints the derived table.
4. Delete the inventory comment at `keymap.go:139-152`; `figaro keys` is now the
   inventory, and it cannot be wrong.

That closes finding 13, fixes the `o`/`M-m` lie and the five letters wrongly
called free, and builds every primitive shape B needs. If Gluck wants one thing
tonight, this is it.

**Shape B, the full thing.** Add, in this order:

5. The action registry, levels and descriptions (118 entries), with the
   pointer-identity completeness test.
6. `keymapSet` as a value behind an atomic pointer; the four structural tests
   become functions over a set (§5).
7. `internal/keyconf`, and the apply pass: merge rule, shadow detection,
   refusal table, diagnostics.
8. `figaro keys bind/unbind/reset/reload`, `figaro doctor keys`, the
   diagnostics row in the help pit.
9. Mode names frozen and documented, one bundled reference page, the
   `ui-stream.md` tables demoted to defaults.

A week of careful work, step 6 the bulk of it. The painting changes at the end
mean tmux and a real pty per the `tmux-testing` skill: a help panel right at one
width and wrong at another is exactly the claim a unit test gets confidently
wrong.

**The permanent tax:** 118 registry names become a compatibility surface
forever. The kebab-case indirection (§3.3) keeps Go renames free; a user-facing
rename breaks files on disk, with no shim.

## 8. Open questions

Gluck's, restated as questions rather than answered:

1. Is `keys.toml` the file, or should `[cli]`/`[cli.tui]` leave `config.toml`
   for a `tui.toml` with the keymap as one table inside it? I argued the keymap
   into its own file (§2); the scalars are a separate call, and
   `plans/tui-config-section-and-sticky.md` §7 question 1 is still open.
2. May remapping reach the `:` box's readline set (`keymap.go:552-660`, about 60
   rows)? Those chords are readline's by contract and a user who moves them
   diverges from every shell they know. I would allow it and warn.
3. Live propagation: a keymap in an in-memory form gets cross-process updates
   free through `internal/cli/intrinsic_mirror.go`, and also becomes visible and
   writable by the agent. Feature or horror? Same question
   `duck/2026-09-16-attendance-form.md` raises about the ring; answer it once.

Scribe's additions, clearly separable:

4. May a chord bind a `:` COMMAND string instead of an action name? `:attend
   <id>` under one key is the obvious ask and it is a different type: the
   registry holds funcs, a command binding holds text. Impossible to retrofit if
   the row has no slot for it, so reserve `command = "…"` now and refuse it in
   v1.
5. `opensPager` (`keymap.go:71-81`) is a per-row policy with a mandatory `why`.
   A user row declares it or inherits it from the action. I lean inherit, and I
   am guessing nobody wants to override it. Ask before the format freezes.
6. `helpID` (`keymap.go:681-724`) is 40 constants that only group panel rows; if
   the registry carries descriptions they become a display group on the action.
   Uncosted, and the grouping is one-to-many in both directions.
7. Key SEQUENCES are out of scope here. `gg` and `f j` are state machines, not
   rows (`t.pendG` at `transcript.go:1796`, `modeFork` at `keymap.go:21`).
   Binding `zz` asks for a prefix map, a second grammar. Refuse in v1, and say
   why in the error.
