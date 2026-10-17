# Keys: layers, actions, and keys.toml

Read this when you want to move a key in the pager, add a chord of your own,
or understand why a key does what it does in one mode and something else in
another. `figaro keys` prints the map in force; this page explains its shape.

## The model, in three words

**Action.** A verb with a meaning in some modes. `down` is a line in the
transcript, the cursor in visual mode, a line of the draft in the composer and
the selection in a list. `cancel` is what Esc means in nine modes. `figaro keys
actions` lists every verb and the modes it means something in.

**Layer.** A set of `keys -> action` over a set of modes. `motions` binds `j`
to `down` once, for the transcript, visual, the composer and lists.

**Precedence.** Layers are ordered, highest first, and the first layer that
binds a key in a mode wins. A binding claims its key only in the modes where
its action means something: `motions` binds `h` to `left`, which the transcript
has no use for, so the transcript's `h` (help) shows through from below.

A **prefix** layer is read by the key after the one that arms it. `g` arms
the `g` layer (so `gg` is `top`), `f` arms `fork`, `c` arms `comment`, Space
arms `leader`. Any key the prefix does not bind is itself, and spends the
prefix.

## Modes

| Mode | When |
|---|---|
| `incipit` | streaming inline; the pager is not up |
| `transcript` | the pager, nothing else open |
| `visual` | `v` / `V`: a cursor or a highlight is up |
| `list` | a pit with rows is open and has the keys |
| `pit` | any pit (`list` included), and the glance pits that are not lists |
| `command` | the `:` line |
| `search` | the `/` box |
| `composer` | the `>` drawer in normal mode (`✎`) |
| `confirm` | a y/n question is up |

`pager` is every mode with the pager up; `all` is every mode.

## keys.toml

Beside `config.toml`. Read once, when figaro starts. Three shapes:

```toml
# Rebind inside a shipped layer. The layer names are the ones `figaro keys` prints.
[bind.motions]
J = "half-down"
d = ""               # unbind: nothing here, and nothing below shows through

# The user layer: above every other, in every mode. `figaro keys bind` writes here.
[bind.user]
z = "sticky"

# Fill the leader.
[bind.leader]
w = "status"         # Space w

# A layer of your own, placed above a named one. Make it a prefix to arm it.
[layer.window]
modes = ["transcript", "pit"]
prefix = true
[layer.window.bind]
s = "status"
q = "queue"

[bind.transcript]
W = "prefix:window"  # W s, W q
```

Keys are spelled as the help panel prints them: `j`, `^D`, `M-m`, `Tab`, `Esc`,
`Enter`, `Space`, `Up`, `PgDn`, `Home`. `^X` claims both encodings a terminal
may send (the control byte and its CSI-u report). Several keys in one entry are
separated by spaces: `"j Down" = "down"`. Two exact spellings exist for the
rare case that needs one encoding only: `0x0e` is the raw byte, `csi:^N` the
CSI-u report alone.

## The verbs

```sh
figaro keys                          # every layer, its modes and bindings
figaro keys actions                  # every verb, and where it means something
figaro keys bind J half-down         # user layer
figaro keys bind w status leader     # into a named layer
figaro keys unbind d motions
figaro keys reset [J [user]]         # forget one of yours, or all of them
figaro doctor keys                   # what the file asked that figaro refused
```

`bind` and `unbind` refuse before they write. A binding that does not take
effect says so on stderr at startup, as a line in the help pit, and in `doctor
keys`, which exits nonzero.

## What is refused

- `^C`, in every layer: it is the guaranteed way out.
- In the `:` line and the `/` box: Enter, Esc, and any printable character an
  action would take from the text.
- A prefix arming another prefix.
- An action that means nothing in any mode its layer covers.

## The help pit

Its key column is prose about the defaults ("j/k · u/d · gg/G") until you move
a key under that row; then it is the live keys, joined. A help row whose every
key you have unbound disappears.
