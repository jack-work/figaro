# Changing the keymap

Read before adding, moving or removing a key binding, or before changing how
the pager or the input loop dispatches one. Using and remapping keys is
[../reference/keys.md](../reference/keys.md).

## Where things are

| File | Holds |
|---|---|
| `internal/cli/keymap_defaults.go` | every action (name, implementation per mode) and every default layer |
| `internal/cli/keylayers.go` | the types and the compiler: layers to rows, shadowing, prefix arming |
| `internal/cli/keymap.go` | modes, chords, the compiled dispatch arrays, the decoder tables, the help rows |
| `internal/cli/keymap_user.go` | keys.toml over the defaults, the refusals, `figaro keys` rendering |
| `internal/keyconf` | the file format, nothing else |
| `internal/cli/testdata/keymap/*.golden` | what the tables compile to and what every key does |

## Adding a binding

1. If the verb exists, add a `bind(...)` to the layer it belongs in. If it
   means something new in a mode it already has a name for elsewhere, add an
   implementation to that action (`atPager(inList, listDown)`) rather than a
   new name: one verb, several modes, is the point of the shape.
2. If the verb is new, add a `named(...)` action. The name is public: a
   reader's keys.toml spells it, so a rename is a breaking change and a Go
   rename is not.
3. `go test ./internal/cli/ -run Keymap -update-keymap`, then read the diff of
   `testdata/keymap/`. It is the review.

## The three goldens

| Golden | Says | Moves when |
|---|---|---|
| `compiled.golden` | mode x chord -> Go function, plus the decoder tables and every prefix table | any binding is added, moved or removed |
| `pager.golden` | starting state x key -> everything the pager can show, and which action the key reached | a binding moves (same signature, new key) or a behaviour changes (new signature, same action) |
| `input.golden` | the same, through the real read loop, with the CSI-u and Meta encodings | anything the input level owns moves |

Record them on a tree you believe in; `-update-keymap` writes what the code
does, so recording over a mistake installs it. The habit that makes them
worth having: before a change that should NOT alter behaviour, run them
unrecorded and expect green; after a change that should, record and read
every moved cell.

A starting state the goldens do not reach is a binding nothing defends.
`TestKeymapGoldenStatesCoverEveryMode` fails when a mode has bindings and no
state; add one to `oracleStates` (and `inputStates` if it involves the read
loop), recorded on the tree BEFORE the change it is meant to referee.

## Rules the compiler enforces

- One binding per mode and chord. Precedence is layer order; there is no
  second conflict rule.
- A binding claims a key only where its action has an implementation, so a
  shared layer never hides a mode-specific key from a lower one.
- A pager-level action never claims incipit: the pager is not up there.
- A prefix layer's bindings resolve in the pager's mode, then the
  transcript's, and any key a prefix does not bind spends it.

## Dispatch that is still code

Three things a table cannot say, all in `transcript.dispatch`: a glance pit
is wiped by any key it does not own; a hosted view's own letters
(`cmdkit.KeyView`, the notifications pit's `f`) are offered after the table;
and with a pit open and the conversation focused (`T`), the transcript's keys
apply. The text boxes fall back to inserting the byte. Everything else is a
binding.

## Proving a real terminal agrees

`scripts/keys-pty.sh` drives the dev-shell binary on a private tmux server
(see [isolated-pane.md](isolated-pane.md)) with a keys.toml that rebinds a
motion, fills the leader, declares a prefix and gets one binding refused, and
checks each in the pane. Each check was canaried: with the binding missing,
it fails.
