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

Beside `config.toml`, read once when figaro starts. Three shapes:

- `[bind.<layer>]` rebinds inside a shipped layer; `key = ""` unbinds it there,
  and nothing below shows through.
- `[bind.user]` is the user layer: above every other, in every mode. `figaro
  keys bind` writes here.
- `[layer.<name>]` declares a layer of your own: `modes`, an optional `above =
  "<layer>"`, `prefix = true` to arm it from a key, and its bindings under
  `[layer.<name>.bind]`. Arm a prefix with `key = "prefix:<name>"`.

[keys.example.toml](keys.example.toml) shows all three as working recipes, and
`figaro keys init` writes it to your config directory as a start. Every recipe
in it is a test: if one stops doing what its comment says, the build fails.

A value that starts with `:` is a **command binding**: the rest is a command
line, run as if typed at `:` on the aria on screen, and its one-line answer
lands in the status bar. It works with the pager up and inline (incipit), and
never in the `:` line, the `/` box or a y/n question. `M-e =
":toggle system.methods.enabled"` turns the reasoning-effort method off and
on again ([methods.md](methods.md)).

Keys are spelled as the help panel prints them: `j`, `^D`, `M-m`, `Tab`, `Esc`,
`Enter`, `Space`, `Up`, `PgDn`, `Home`. `^X` claims both encodings a terminal
may send (the control byte and its CSI-u report). Several keys in one entry are
separated by spaces: `"j Down" = "down"`. Two exact spellings exist for the
rare case that needs one encoding only: `0x0e` is the raw byte, `csi:^N` the
CSI-u report alone.

## The verbs

```sh
figaro keys init                     # write keys.example.toml as your keys.toml
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

## The shipped layers

Highest first. Generated from the defaults; `figaro keys` prints the same with
your changes applied.

<!-- generated: layers -->
| Layer | Modes | Bindings |
|---|---|---|
| confirm | confirm | `y` yes · `Y` yes · `n N` no · `Esc` cancel |
| search | search | `Enter 0x0a` activate · `Esc` cancel · `Backspace ^H` backspace |
| command | command | `Enter 0x0a` activate · `Esc` cancel · `M-Enter` accept-snap · `^A` cmd-home · `^E` cmd-end · `^B` cmd-left · `^F` cmd-right · `M-b` cmd-word-left · `M-f` cmd-word-right · `^L` cmd-redraw · `^D` cmd-delete · `^T` cmd-transpose · `M-t` cmd-transpose-word · `M-u` cmd-upcase · `M-l` cmd-downcase · `M-c` cmd-capitalize · `^K` cmd-kill-to-end · `^U` cmd-kill-to-start · `^W` cmd-kill-word · `M-d` cmd-kill-word-forward · `M-DEL` cmd-kill-word-alpha · `M-\` cmd-delete-space · `^Y` cmd-yank · `M-y` cmd-yank-pop · `^P Up` cmd-history-prev · `^N Down` cmd-history-next · `M-<` cmd-history-first · `M->` cmd-history-last · `^R` cmd-search-prev · `^S` cmd-search-next · `M-p` cmd-prefix-prev · `M-n` cmd-prefix-next · `M-. M-_` cmd-yank-last-arg · `^_` cmd-undo · `M-r` cmd-revert · `^G ^C` cmd-abort · `Tab` cmd-complete · `^V` cmd-paste · `M-?` cmd-list-completions · `M-*` cmd-insert-completions · `Backspace ^H` backspace · `Home` cmd-home · `End` cmd-end · `Left` cmd-left · `Right` cmd-right |
| composer | composer | `i` insert · `a` insert-after · `I` insert-home · `A` insert-end · `h` left · `Home` line-start · `End` line-end · `v` mark-char · `V` mark-line · `/` search · `?` search-back · `n` find-next · `N` find-prev · `F` fullscreen · `:` command · `Esc` cancel · `M-Enter ^S` send |
| visual | visual | `Esc` cancel · `Enter 0x0a` activate · `a` attend · `Y` yank-coordinate · `H` screen-top · `M` screen-middle · `L` screen-bottom · `n` find-next · `N` find-prev |
| pit | pit,list | `a` attend · `Enter 0x0a` activate · `Esc` cancel · `x` delete · `X` delete-now · `n` find-next · `N` find-prev |
| list | list | `e` scroll-down · `F` fullscreen · `0x0e` down · `0x10` up |
| transcript | transcript | `f` prefix:fork · `c` prefix:comment · `a` attend · `^` attend-parent · `^O` aria-back · `Tab` aria-forward · `Enter 0x0a` activate · `e` toggle-bodies · `Esc` cancel · `x` delete · `0x0e` select-next · `0x10` select-prev · `n` find-next · `N` find-prev |
| pager | transcript,pit,list | `M-n` question-next · `M-p` question-prev · `s` sticky · `D` decisions · `Space` prefix:leader · `l` list · `L` list-home · `H` hang-up · `S` form · `T` focus · `h` help · `!` status · `Q` queue |
| reading | transcript,pit,visual,list | `q` leave · `m` bar-verbose · `/` search · `?` search-back |
| conversation | transcript,visual | `v` mark-char · `V` mark-line · `X` hang-up-drop · `:` command · `>` compose |
| clipboard | incipit,transcript,pit,visual,composer,list | `y` yank |
| motions | transcript,visual,composer,list | `j` down · `k` up · `d` half-down · `u` half-up · `G` bottom · `g` prefix:g · `Down` down · `Up` up · `PgDn` half-down · `PgUp` half-up · `Home` top · `End` bottom · `h Left` left · `l Right` right · `w` word-next · `b` word-prev · `e` word-end · `0` line-start · `^` line-first · `$` line-end · `{` para-prev · `}` para-next |
| session | all | `^C` interrupt · `^D` detach · `^L` listen · `^T` listen · `M-m` verbose · `csi:^N` select-next-extend · `csi:^P` select-prev-extend |
| g (prefix) | transcript,visual,composer,list | `g` top |
| fork (prefix) | transcript | `j Down` fork-next · `k Up` fork-prev · `Esc` clear-note |
| comment (prefix) | transcript | `j Down` comment-next · `k Up` comment-prev · `Esc` clear-note |
| leader (prefix) | transcript,pit,list | `n` notifications |
<!-- end: layers -->

## The actions

<!-- generated: actions -->
| Action | Means something in |
|---|---|
| `interrupt` | all |
| `detach` | all |
| `listen` | all |
| `verbose` | all |
| `select-next-extend` | all |
| `select-prev-extend` | all |
| `leave` | transcript,pit,visual,list |
| `bar-verbose` | transcript,pit,visual,list |
| `question-next` | transcript,pit,list |
| `question-prev` | transcript,pit,list |
| `sticky` | transcript,pit,list |
| `yank` | incipit,transcript,pit,visual,composer,list |
| `yank-coordinate` | visual |
| `hang-up` | transcript,pit,list |
| `hang-up-drop` | transcript,visual |
| `down` | transcript,visual,composer,list |
| `up` | transcript,visual,composer,list |
| `half-down` | transcript,visual,list |
| `half-up` | transcript,visual,list |
| `top` | transcript,visual,composer,list |
| `bottom` | transcript,visual,composer,list |
| `left` | visual,composer |
| `right` | visual,composer |
| `word-next` | visual,composer |
| `word-prev` | visual,composer |
| `word-end` | visual,composer |
| `line-start` | visual,composer |
| `line-first` | visual,composer |
| `line-end` | visual,composer |
| `para-prev` | visual,composer |
| `para-next` | visual,composer |
| `screen-top` | visual |
| `screen-middle` | visual |
| `screen-bottom` | visual |
| `search` | transcript,pit,visual,composer,list |
| `search-back` | transcript,pit,visual,composer,list |
| `find-next` | transcript,pit,visual,composer,list |
| `find-prev` | transcript,pit,visual,composer,list |
| `activate` | transcript,search,command,pit,visual,list |
| `cancel` | transcript,search,command,pit,visual,confirm,composer,list |
| `yes` | confirm |
| `no` | confirm |
| `backspace` | search,command |
| `attend` | transcript,pit,visual,list |
| `attend-parent` | transcript |
| `aria-back` | transcript |
| `aria-forward` | transcript |
| `fork-next` | transcript |
| `fork-prev` | transcript |
| `comment-next` | transcript |
| `comment-prev` | transcript |
| `clear-note` | transcript |
| `notifications` | transcript,pit,list |
| `command` | transcript,visual,composer |
| `compose` | transcript,visual |
| `help` | transcript,pit,list |
| `status` | transcript,pit,list |
| `queue` | transcript,pit,list |
| `decisions` | transcript,pit,list |
| `list` | transcript,pit,list |
| `list-home` | transcript,pit,list |
| `form` | transcript,pit,list |
| `focus` | transcript,pit,list |
| `delete` | transcript,pit,list |
| `delete-now` | pit,list |
| `select-next` | transcript |
| `select-prev` | transcript |
| `toggle-bodies` | transcript |
| `mark-char` | transcript,visual,composer |
| `mark-line` | transcript,visual,composer |
| `insert` | composer |
| `insert-after` | composer |
| `insert-home` | composer |
| `insert-end` | composer |
| `fullscreen` | composer,list |
| `scroll-down` | list |
| `send` | composer |
| `accept-snap` | command |
| `cmd-home` | command |
| `cmd-end` | command |
| `cmd-left` | command |
| `cmd-right` | command |
| `cmd-word-left` | command |
| `cmd-word-right` | command |
| `cmd-redraw` | command |
| `cmd-delete` | command |
| `cmd-transpose` | command |
| `cmd-transpose-word` | command |
| `cmd-upcase` | command |
| `cmd-downcase` | command |
| `cmd-capitalize` | command |
| `cmd-kill-to-end` | command |
| `cmd-kill-to-start` | command |
| `cmd-kill-word` | command |
| `cmd-kill-word-forward` | command |
| `cmd-kill-word-alpha` | command |
| `cmd-delete-space` | command |
| `cmd-yank` | command |
| `cmd-yank-pop` | command |
| `cmd-history-prev` | command |
| `cmd-history-next` | command |
| `cmd-history-first` | command |
| `cmd-history-last` | command |
| `cmd-search-prev` | command |
| `cmd-search-next` | command |
| `cmd-prefix-prev` | command |
| `cmd-prefix-next` | command |
| `cmd-yank-last-arg` | command |
| `cmd-undo` | command |
| `cmd-revert` | command |
| `cmd-abort` | command |
| `cmd-complete` | command |
| `cmd-paste` | command |
| `cmd-list-completions` | command |
| `cmd-insert-completions` | command |
<!-- end: actions -->
