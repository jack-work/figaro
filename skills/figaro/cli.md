# The command surface

Every verb, and what it actually does. The nine daily gestures are in
[SKILL.md](SKILL.md); this file is where you come for the rest, or for a flag's
exact meaning.

These hold across the whole CLI:

- **Flags go after the verb.** `figaro send -f -- <prompt>`, never
  `figaro -f send -- <prompt>`. The only tokens figaro reads *before* a verb
  are `--version`/`-V`, `--help`/`-h`, and the binding overrides
  `--no-bind`/`--absolute`/`-A`/`--bind`; anything else there is an unknown
  command, and gets a did-you-mean rather than a guess. The bare prompt form
  (`figaro [flags] -- <prompt>`) is the one exception, and it takes `send`'s
  flags because it *is* `send`.
- **`--` separates flags from a prompt.** Flags go before it, prompt after. A
  `--` inside the prompt body is prompt text, and nothing before the boundary
  is dropped in silence, an unrecognized token is an error.
- **Targeting** is `--id <id>` everywhere, or a positional `<id>` on most
  verbs. With neither, a verb targets the figaro this shell is attended to. The
  precedence is `--id` beats `FIGARO_ARIA` beats the pid binding.
- **`-j`/`--json`** submits and exits, printing one machine-readable line on
  stdout and nothing else. It contradicts anything that streams or renders
  (`-r`, `-v`, `-o`, `-l`, `-x`), and says so rather than dropping them.
- **`-h`/`--help`** prints that verb's help on **stdout**, exit 0: reserved on
  every verb, so no command may claim `-h` for itself. Usage printed because
  argv was wrong goes to **stderr** with **exit 2**; exit 1 means the command
  ran and failed. `figaro help [<verb>]` is the same help by another door.

## Prompting

| Command | Effect |
|---|---|
| `figaro -- <prompt>` | Prompt the attended figaro. Creates one if this shell has no binding. |
| `figaro send [flags] -- <prompt>` | The same, with the verb spelled out. Alias `qua`. |
| `figaro new [-O <names>] [-S <k=v>] -- <prompt>` | Mint a fresh figaro, bind this shell to it, prompt it. |
| `figaro new -C [<@form>]` | Mint the figaro and CAST it in one gesture. With a role named, `-O`/`-S` dress the figaro; with none, they mint the role (`new -CO reviewer`, `new -CS name=x`). When the call MINTS a role, the shell ends up attending that role (`--stay` opts out); a role redirects turn-shaped verbs to its holder, so prompts still reach the figaro. A figaro minted but not cast is reported as that partial, with its id. |
| `figaro new` | With no prompt: mint on the default outfit and attend it, no turn. `new` always mints; `figaro attend null` is how you go home. In the pager, `:new [-O] [-S] [-D]` does the same and shows the new figaro there. |
| `figaro` | At a terminal, with no arguments: `figaro listen`. Off a terminal: the usage. |
| `figaro decide [-C <a,b>] -- <question>` | Ask a decision model (jev or luna) a yes/no or multiple-choice question over the attended figaro's conversation. Reads only; prints the scored answer. See [reference/decisions.md](reference/decisions.md). |

`send` flags, all combinable unless noted:

| Flag | Meaning |
|---|---|
| `-f`, `--forget` | Submit and exit. Do not attach to the stream, do not interrupt on Ctrl-C. The turn keeps running in the daemon. Mints a figaro if this shell has none (the id goes to stderr, or to stdout with `-j`). |
| `-O`, `--outfit <names>` | Dress the figaro in OUTFIT NAMES. On one this call creates it is the birth outfit; on one that already exists it is folded onto the form **in the same call as the prompt**, so that turn is answered wearing it. Repeats append (`-O a -O b` = `-O a,b`). Bundles like any value-taking short: `-rO sonn5` or `-rOsonn5`. Defaults to `default_outfit`. See [reference/outfits.md](reference/outfits.md). |
| `-S`, `--set <k=v>` | Form keys, the other axis: `k=v` or a JSON literal, comma-separated. Repeats append (`-S a=1 -S b=2` = `-S a=1,b=2`), a later key winning. Folded AFTER `-O`, so your key beats the outfit's. |
| `-D`, `--delete <paths>` | Form key paths to remove, comma-separated. Repeats append. Folded last. |
| `-r`, `--raw` | Plain text on stdout: no ANSI, no markdown. Streamed, not buffered. |
| `-o`, `--verbose` | Start with more on: names beside their other spelling, node addresses. `M-m` toggles it live. |
| `-l`, `--listen` | Open the transcript pager at startup. |
| `-v`, `--verbatim` | Dump raw wire frames as JSON, one object per line. |
| `-x`, `--exec` | Treat the reply as a bash script and run it. `-n` prints without running, `-y` skips the confirmation. |
| `-j`, `--json` | Submit, print one line `{aria_id, mode}`, exit. Contradicts `-r`/`-v`/`-o`/`-l`/`-x`. |
| `--stdin` | Read the prompt from stdin, never argv, so the text stays out of the process table. A prompt after `--` beside it is refused, as is `-x`. |
| `--image <file>` | Attach a png, jpeg, gif or webp ahead of the prompt's text; repeat for up to 4, 10 MB each. The daemon sniffs the bytes and shrinks a picture to fit the message, so a figaro with `system.tools = []` still sees it. A picture that already fits is sent byte for byte: EXIF, including GPS location, is NOT removed; strip it first if it matters. Needs a prompt; refused with `-x`, `-v`, fork and the pager. |

`-O`, `-S`, `-D` and `--image` repeat; `--id` or `--record` given twice is
refused.

Every figaro persists. A one-off is an ordinary send that mints and states its
own lifetime: `figaro send -A -f -S system.ttl=1h -- <prompt>`, or `-r` in
place of `-f` to stream plain text into a pipe. `-A` ignores this shell's
binding, so the send mints instead of steering the attended figaro. Inside an
figaro's own shell `FIGARO_ARIA` outranks `-A` and the send reaches you; mint
with `new` there ([subagents.md](subagents.md)).

Timing is not a flag. A prompt that arrives while a turn is running joins it as
a steering aside; a prompt that arrives when nothing is running opens a turn.
The classification happens where the queue is drained, and nowhere else.

## The queue

**Every drain coalesces the contiguous run of queued prompts into one message;
a queued `set` or `fork` is a barrier.** Four chained sends against an idle
figaro are one reply and then ONE combined question: not four turns to sit
through, and the same is true of messages queued behind a turn you then
interrupt. The messages are separated by a blank line, so they stay separate
lines on screen and separate messages to the model. A `set` queued between two
of them still applies in order, because folding a prompt in front of it would
answer that prompt against a form it was never written against.

A single submit is untouched: one message in, one message out.

Prompts that arrive while the figaro is busy wait in its **queue**. They are
addressed by an id from the listing, paired with the **epoch** that id was read
in: ids restart whenever the agent is rebuilt (a daemon restart, a
dormant→attach), so every mutation re-reads first and hands the epoch back. A
request from a previous generation is refused as `stale` rather than resolved
against whatever holds that number now.

| Command | Effect |
|---|---|
| `figaro queue [--id <figaro>] [-j]` | List: id, state, age, text. |
| `figaro queue rm <id>...` | Drop those messages. |
| `figaro queue rm --all` | Drop all of them. |
| `figaro queue edit <id> -- <text>` | Rewrite one. |

To ADD to the queue, `send`, a queued message is just a prompt that arrived
while the figaro was busy, so there is no separate create verb. The sub-verb owns
the positional slot, so another figaro is `--id`, never a bare id.

A refusal is an **answer**, not a crash. The agent declines to delete a message
it has already committed, and says which: `committing` (lifted by the drain
loop this instant), `committed` (already part of the conversation), `merged` (an
interrupt folded it into another queued message: the survivor's id is given),
`stale`, `unknown`, `closed`. Exit is 0 when every id was applied, 1 when any
was refused, 2 for misuse.

## Hanging up

Two verbs, differing in one thing: what becomes of the queue.

| Command | Effect |
|---|---|
| `figaro hup [<id>] [-j]` | Stop the turn, **keep** queued messages. |
| `figaro hup -d [<id>] [-j]` | Stop the turn and **discard** them. |
| `figaro cut [<id>] [-j]` | Shorthand for `hup -d`. |

Both forms **return** the queued messages: listed on stdout, or as JSON with
`-j`: so discarding is not losing.

`hup` is the everyday one, and what it leaves behind is governed by the rule
below.

`cut` hands back what it dropped, verbatim, one entry per message as you typed
it, with the form input each carried:

    figaro cut -j > lost.json

Clearing does not need a turn to be running. Neither verb touches a queued
`set` or `fork`: they drop questions, not someone else's control events.

In the transcript pager, **`H`** is `hup` from the keyboard and **`X`** is
`hup -d`: both stop the turn and keep watching. They are the third thing you
can do to a running turn: `Ctrl-C` stops it and exits, `Ctrl-D` exits and lets
it run on, `H`/`X` stop it and stay. What `X` drops is printed into the status
line and reprinted to the shell when you leave the pager, so the text survives
even though its place in the queue does not.

The wire behind all of this: the interrupt's queue disposition, the
`(epoch, id)` identity, and the closed set of refusal reasons: is
[reference/ui-stream.md](reference/ui-stream.md).

## Moving around

| Command | Effect |
|---|---|
| `figaro ls [<id>]` | List figaros. Attended, the default scope is you and everything forked below you; detached it is home. `<id>` names another subtree, `/` is home. Alias `list`. Through a troupe door, this node's rows come from its own daemon, not from the door. Without one, it lists this node only and says how many peers it did not ask. |
| `figaro ls ^` / `^2` / `^^` | Climb N layers: your parent's subtree, you still in it. Past the root it clamps to the top-level figaro and prints a note. |
| `figaro ls -H` | Home view: every top-level figaro, without unbinding you. (`-h` is help, on every verb.) |
| `figaro lsh` | `ls -H`, spelled shorter. Same flags. |
| `figaro ls -g` | Home plus the null root and outfit anchors. |
| `figaro ls -a` / `-n N` | Remove the 10-row cap, or set it. Mutually exclusive. |
| `figaro recent [-n N] [-j]` | Every figaro on this node, most recently active first, no tree: each row is its handle's first word (the whole handle when two rows share it), its mantra, and how long ago it was active. The pager's `M-l` and `:recent` show the same list as a pit. |
| `figaro attend <id>` | Bind this shell to a figaro. Alias `at`. |
| `figaro attend <id>:<turn>` | Bind with a pending fork point: the next bare prompt forks there. `<id>.<lt>` names an LT instead of a turn; `:head` alone drops the pending fork point. |
| `figaro attend <node>/<id>` | Bind this shell to a figaro on another node. The id is resolved on that node, through the door of the `troupes/*.toml` profile that answers for it, and this node's daemon keeps the binding (the node must be a troupe member). Plain `figaro`, `listen` and `figaro -- <prompt>` in this shell then reach that figaro through the same door with that profile's login; `ls`, `new` and every verb that names no figaro stay on this node. `:<turn>` is refused there. |
| `figaro attend null` | Go home. There is no `detach` verb. |
| `figaro back [<n>]` / `figaro forward [<n>]` | Move this shell's attendance along its history (ctrl+o / Tab in the pager). Every attend is an entry; an attend after a back drops what was ahead; the newest 50 are kept; a figaro killed or reaped since is passed over and dropped. The daemon keeps it with the attendance, so every pager on the shell shares it. |
| `figaro show [<id>]` | Render history: the same rows `listen` draws. `-n N` last N turns, `-a` all, `-o` block addresses and timestamps, `-v` raw IR, `-l` no markdown, `-j` JSON. |
| `figaro status [<id>]` | One node in focus, whichever species it is. A FIGARO: provider, model, context, cost. A FORM: its name, version, key count, and that it is not cast. A ROLE: the same plus `target-aria` and the state of the figaro it points at (live, dormant, or missing). `-m` adds cwd/outfit/fork origin for a figaro, and parent plus the keys held for a form. `-j` carries `species`. |
| `figaro listen [<id>]` | Attach to the live stream without prompting. `<node>/<id>` on another node listens through that node's door (see the troupe file below). With no id and no figaro attended, mints one from the default outfit and attends it. On an unbound form (named, or attended with `fig at @form`) it opens the pager on the form's pit, live; a prompt there is refused, since a form has no turn loop. A role opens on the figaro that holds it. Opens in the transcript pager (no inline view); `q` with no pit open, or Ctrl-D, ends it and the turn survives. Esc does not end it, and Ctrl-T does nothing there. A daemon restart does not end it: it waits for the next angelus and follows on. |
| `figaro hup [<id>] [-d]` | Hang up: stop the turn; `-d` also discards the queue (see The queue). |
| `figaro cut [<id>]` | Shorthand for `hup -d`; `-j` returns the messages. |
| `figaro queue [rm\|edit]` | Read, edit and delete what has not been answered yet. |

`ls` columns: ARIA (mantra, with `▸` running, `●` this shell, `○` idle), ID
(the figaro's word handle), OUTFIT, VER, FORK, AGE, MSGS, CTX, CWD. Running
outranks this shell: while your own turn is in flight your row shows `▸`, so
identify yourself from the header ("attending ...") instead.

`attend` binds the **terminal**, not the process that typed it: on unix the key
is the session leader, so subshells, pipelines, `$(...)`, `timeout figaro …`
and a shell-prompt segment all resolve to the same figaro. A script run from an
attended shell therefore speaks to that shell's figaro: it cannot move the
attendance, only use it. Windows has no POSIX session and keys on the parent.
Override with `--no-bind`/`-A` (ignore the binding) or `--bind` (use it where
the no-TTY rule would switch writes off); both go before the verb.

### Naming a figaro

Every `<id>` above takes any of these, and all of them name the same figaro:

| Form | Example |
|---|---|
| the full id | `a5647bfbc216b8a09ffea9586393c289` (old figaros: `d9a9cebf`) |
| its word handle | `nursery-lioness` |
| the handle's stems, 3+ letters a word | `nur-lio` |
| any unique prefix of the handle, 3+ letters | `nurs` |
| any unique hex prefix, 4+ characters | `a5647` |
| either, on another node | `spain2/nursery-lioness` |
| a form | `@` before any of them |
| a form by its `name` key | `@almaviva` |

`@<name>` is tried only when no id or handle answers. It matches the `name`
key exactly, among the forms you may read: a role first, then a form not yet
cast, which is what `figaro cast <figaro> @<name>` takes. A name two roles (or,
with no role, two forms) carry is refused, naming both by handle. A form held
in a box answers its name only inside that box; from outside, use its handle. In the
form slot of `cast`, `study` and `drop`, a name nothing carries is refused as
`no role or form named @<name>`.

The two words spell the id's first five hex digits, so the handle and the
five-character prefix are one name written two ways. A new id is redrawn at
mint until its first five digits are its own, so both stay unique for good.
Two old ids that share five digits share two words; each gets a third
(`nursery-lioness-refinery`) and the two-word form is refused as ambiguous,
naming both. `ls`, the status bar and every "attending" line show the handle;
`status` shows the handle, the short hex and the full id; `-j` carries
`id`, `short` and `handle`. Tab completes handles, newest first, 50 at a
time until you type; typed hex completes to full ids instead.

`ids = "short"` under `[cli]` in `config.toml` shows the five-digit hex
instead of the handle wherever a person reads a name: the pager, the bar,
every CLI line. `-j` keeps `id`, `short` and `handle` as they are and adds
`display`, the name the setting shows, for scripts that print one, and
`figaro name [<id>] [-m]` prints it alone (`-m` adds the other spelling): sign
a herald footer with `--sender-id "$(figaro name)"`. In the
pager `y` copies the name shown (the attended figaro's, or a listing row's; in
`l` and `M-l` a figaro row's handle, node-qualified on a peer's row) and `M-y`
the full id.

## Branching

| Command | Effect |
|---|---|
| `figaro fork` | Branch the attended figaro at its head. |
| `figaro fork <id>:12` | Interior fork: history through turn 11 is shared, the original suffix continues, a fresh empty alternative diverges. |
| `figaro fork [flags] -- <prompt>` | Branch and immediately prompt the new branch. Takes `send`'s flags. |
| `figaro fork --stay` | Branch without moving this shell. |
| `figaro promote <id> [levels]` | Raise a figaro in the tree `ls` draws; it takes its parent's place. Presentation only. |
| `figaro succeed <@role> [--turn <n> \| --successor <figaro>] [--title <t>] [--ttl <d>] [--abdication retire\|linger] -- <prompt>` | Hand a role to a successor: fork the figaro it targets at `--turn` (default 2) or take `--successor`, demote the predecessor beneath it, prompt and cast the successor, then retire the predecessor with a ttl (default 10m; `0` kills it). `linger` leaves it promptable for an overlapping handover. Not atomic: a failure names the commands that finish the job. `:succeed` in the pager follows the successor unless `--stay`. See [reference/forms.md](reference/forms.md#roles). |
| `figaro demote <id> [--under <child>]` | Sink a figaro to a leaf: a child takes its place, the other children move under that child, and a kill then takes the figaro alone. Presentation only. |
| `figaro kill <id> [-r] [--when <d> \| --now]` | Stop a trunk's agent and sessions, then reap it (and with `-r` its branches, which a kill without `-r` refuses) until its grace runs out: `--when` (`90m`, `6h`, `2d`, `1w`), else `[memory] kill_grace_hours`, three days. `--now` or `--when 0` deletes at once. Killing a reaped figaro moves its group's purge to the new grace. |
| `figaro reaped [ls \| restore <id> [--keep] \| purge <id>]` | What `kill`, `gc` and the ttl sweep reaped, who reaped each group, when it is purged, and the way back. |
| `figaro retire <id> [--ttl <d>] [-O ..] [-S ..] [--undo]` | Stop a figaro taking prompts. Reads and forks still work; a fork is not retired. The outfits and keys land in the same write; `--ttl 1h` reaps it an hour from now unless something holds it. |
| `figaro export [<id>] [-o <f>]` | Write a figaro to a portable file: outfit, form, every message. |
| `figaro import <file>` | Restore one into THIS store as a new conversation. `-` reads stdin. |

The model behind these, including what freezes and which child keeps the id, is
[reference/trunks.md](reference/trunks.md).

**Moving a figaro between stores**: out of a dev shell, onto another machine -
is `export` then `import`. They carry CONTENT, not identity: node ids, fork
bases and LTs belong to the store a figaro is in, so the destination mints its
own and an import can never collide with what is already there. Three things do
not travel: the figaro's id (a trunk id is unique per store: the old one is
reported as provenance), branches, and the provider translation caches, so the
first turn after an import re-translates. Preserving all three needs a byte-level
graft, which is designed in [proposals/aria-graft.md](../../proposals/aria-graft.md)
and deliberately not built: an import that goes wrong refuses, while a graft
that goes wrong yields a store that looks fine.

## State

| Command | Effect |
|---|---|
| `figaro state [show] [<id>]` | The folded form snapshot. `-j` for JSON. Alias `form`; `show` is the explicit spelling. |
| `figaro set [<id>] <key> <value>` | Patch one key with no model round trip. |
| `figaro unset [<id>] <key>...` | Remove keys. |
| `figaro toggle [--id <id>] <key>` | Flip a true/false key. Absent on the figaro, it reads the default outfit's value, else false. Refuses any other value. |
| `figaro form set <key> <value>` | The same, from the form family. `form set a=1,b=2` takes the `-S` grammar instead. |
| `figaro form delete <paths>` | Remove key paths, comma-separated (the `-D` grammar). `unset` is its older name. |
| `figaro form help <topic>` | A help page, from the form family's third position. |
| `figaro get [--id <id>] [-j] <path>...` | Print values by the paths `set` takes. A string prints bare, other scalars as JSON, an object or array as indented JSON; `-j` makes every value JSON, and several paths one object keyed by path. A missing path exits 1 with `no key <path>`. `figaro .<path>` and `figaro show .<path>` are the same verb; `.N` is an LT, so it is refused. In the pager, `:get <path>` puts a short value on the bar and a long one in a pit. |
| `figaro pwd [--id <id>]` | Print `system.cwd`, or the daemon's own directory when it is unset (where the tools then run). The pager resolves a relative path you open in `$EDITOR` against the same directory, and refuses a figaro on another node. `:pwd` in the pager. |
| `figaro state outfit <names>` | Fold outfits onto this figaro now: additive, nothing removed, a `<system-reminder>` for what changed. Takes NAMES only: keys are the other axis (`form set`). The names travel as names and the daemon resolves them once, at its API boundary, so the same `-O` means the same thing at birth and on a live figaro. `--list` for what is on disk. See [reference/outfits.md](reference/outfits.md). |
| `figaro gc [<selector>] [--yes \| --dry-run] [-j]` | Plan, then reap the figaros the selector chooses: `--idle 30d`, `--empty`, `--older 1d`, `--outfit <name>`, `--under <id>`, `--match <fzf query>`, `--box <name>`, ANDed. Prints candidates, what is kept and why, and the bytes freed at purge; asks on a terminal, changes nothing piped without `--yes`. With no selector it collects outfit versions no aria was born under (content-addressed, so the next aria wanting one re-mints it). |
| `figaro gc --undo <run>` | Restore every group one gc run reaped. |
| `figaro form listen [<id>]` | Watch the form as a live JSON tree, in the alt screen. The client keeps its OWN copy and applies the patches the server broadcasts (`form.delta`), so it is the same tree and the same algebra on both ends. `j`/`k` move, `enter` expands, `y` yanks, `e`/`d` page. |
| `figaro state outfit --tree [<spec>]` | Draw the layer closure and apply nothing: green where a layer resolves, red where it does not. The angelus resolves it: the outfits directory is the server's state, so a client asks rather than reading it. Exits non-zero when the closure has a gap. |

Setting a key is a real event in the conversation: on the tic where a
non-`system` key changes, the agent sees a `<system-reminder>` naming it. Keys
under `system.` are hidden from the agent and read directly by the harness.

**Lifetimes.** `figaro set --id <id> system.ttl 30d` gives a node a lifetime:
the daemon's reclamation sweep deletes that node and its subtree once the
lifetime is spent. `-S system.ttl=1h` states one at birth, on `new`, `fork`, or
a `send` that mints, and an outfit's `[system] ttl` does the same for every
figaro dressed in it (the shared default form keeps none of its own). It is counted from when the node was CREATED, not from its
last turn, and a fork inherits the key with its own creation time. Go duration
syntax plus `d` and `w`; `0`, an empty value or `unset system.ttl` means no
lifetime and nothing is ever deleted.

A spent lifetime does not delete a node in use. `system.keepalive` (same
syntax; default one day, `[memory] keepalive_minutes`) keeps a node whose
lifetime is spent while it was used within that window. Use is a successful
call that names the figaro: a prompt, a `set`, a read or `show`, attend, bind,
listen, a fork from it, a queue read. `ls`, the tree view, the pager's topology pit, status
and `doctor ttl` are not use, and neither is a call that was refused. Records
nobody asked for (translation catch-up, study marks) do not count either: a
node is measured from its last use, and from its newest record only until it
is first used under 0.50. `system.keepalive=0` gives reaping at the deadline.
The sweep runs on `[memory] sweep_interval_seconds` (two minutes by default)
and also passes over a node when anything its delete would take (the node and
its branches) has a live agent, a bound shell, a role pointing at it, a prompt
spooled across a restart and not yet delivered, or a use within its own
keepalive. A retired figaro (`figaro retire`) is held only by a
keepalive it states itself, so `retire --ttl 1h` really ends it in an hour.

`system.reap_if_empty = true` reaps a figaro that never had a message: no
prompt, no reply. Empty means no messages, not no state: a figaro used only as
a board (keys set, no turns) is reaped too. Once it is a minute past its birth
and its last use, the sweep reaps it the way a spent lifetime would, unless it
has a live agent, a bound shell or a role pointing at it, a branch under it,
or a spooled prompt. The first message
ends the matter: from then on the key does nothing, and `system.ttl` still
applies on its own. Set it at birth (`-S system.reap_if_empty=true`) or in an
outfit's `[system]`, so a figaro minted and left unused does not pile up;
first-run setup writes it into the starter outfit, so on a fresh install that
is every new figaro, boards included. A shell still attending it holds it until
the shell exits or attends elsewhere.

What the sweep takes, it first reaps rather than deletes: the node and
everything its delete would take get `system.reaped` (naming the root), leave
`ls`, refuse prompts, and are indexed in the catalog `@reaped`
(`figaro form show @reaped`). `[memory] reaped_grace_hours` later (seven days by
default; `0` deletes at once) the sweep purges the group, unless it has a live
agent, a bound shell or a role pointing in. `figaro reaped` lists the groups,
`figaro reaped restore <id>` brings one back (the keepalive then holds it a
day; `--keep` also unsets its ttl), and `figaro reaped purge <id>` deletes it
now. A fork of a reaped figaro is not reaped.

`figaro kill` reaps the same way, but the group carries its own deadline
(`system.reaped_until`, the kill time plus `--when` or `[memory]
kill_grace_hours`), and at that deadline it is purged whatever holds it: a kill
is a delete deferred, not a request. A sweep-reaped group keeps
`reaped_grace_hours`; killing one gives it the kill's deadline.

`figaro gc <selector>` chooses figaros for you and reaps them the way `kill -r`
does, with the kill's grace, each group stamped `system.reaped_by =
"gc:<run>"` so `figaro gc --undo <run>` brings the run back. It never takes a
figaro that has, or whose delete would take one that has, a live agent, a
bound shell or attending pane, a role pointing in, a spooled prompt, a
background session, a use within its keepalive, or `system.keep = true`
(owner-held); nor one whose delete would take a figaro the selector did not
choose. Those are listed as kept. Apply runs the selector again: a figaro
used, held or forked since the plan is dropped from the run and named. A
plan lasts ten minutes and only its caller applies it. Unboxed, gc is the
owner's (`admin`); a box plans and reaps only its own figaros, and the
owner's plan skips boxes unless `--box` names one.

`ls`, the tree view and the pager's topology pit mark a row `[reaps in 3h]` inside the
last quarter of its keepalive (at most six hours), `[reaping]` once it is due
and only held, and `[retired]`; a row past its reap time that nothing holds,
and every reaped row, is omitted, with a count of what was withheld.
`figaro doctor ttl` reports every lifetime with when the sweep may take it, and
`ls -j` carries `ttl_ms`, `expires_at`, `reaps_at`, `keepalive_ms`,
`retired_at` and `reaped_at`.

Two `system.` keys are exceptions in one direction: they do not render
themselves, they change what OTHER events say.
`system.study_incantation` is `{onstudy, onupdate, ondrop}`, any subset, each a
string, and the matching phrase rides that study block as a `say` field.
`system.fork_incantation` is the same for a branch's birth: a bare string, or
`{"onfork": …}`. Unset means silence. A malformed one is logged and skipped
key by key; the fact it decorates still reaches the model. Both are read from
the BOUND form only: a studied form does not get to put words in its
observer's mouth.

## Daemon and tools

| Command | Effect |
|---|---|
| `figaro stop` | Shut down the angelus and retire its hush agent (see The vault). Alias `rest`. `-f` to SIGKILL, `-k` when a restart is coming, `--forget-bindings` to start the next angelus with no shell attended. |
| `figaro doctor gc` | Remove dead store channels. `-n` to report without touching. Requires the daemon stopped. |
| `figaro doctor schema` | Report per-channel format versions. |
| `figaro doctor normalize [-n] [-j]` | Make every promoted figaro own its history, then drop the `@topology` overrides that change no delete set (figaro gone, or naming exactly its history parent), each with its reason. `-n` reports both halves without touching anything. Blocks while it copies; meant for off hours. See [reference/trunks.md](reference/trunks.md). |
| `figaro doctor mem [-j]` | What the daemon is holding: live/resident figaros, IR cache, endpoints, attached clients, heap. See [contributing/reclamation.md](contributing/reclamation.md). |
| `figaro doctor ttl [-j]` | Every node that states a lifetime (`system.ttl`), when each one ends, which a keepalive still holds, which are due, and which the sweep keeps past their lifetime (held: by which node, why, since when). The sweep logs a held node once per holder and reason. |
| `figaro keys [init\|actions\|bind\|unbind\|reset]` | The pager's keybindings, by layer, and `keys.toml` behind them. `init` starts one from the shipped recipes. See [reference/keys.md](reference/keys.md). |
| `figaro doctor snapshot <dir> [--from <store>] [--arias N] [--max-mb M] [-j]` | Copy a slice of a store (default: this one) into a new store at `<dir>`: the `--arias` (default 50, `0` = all) most recently active arias with every node their history reads, and every form and outfit, under `--max-mb` (default 500, `0` = no cap). Reads the source and never writes it, so it runs beside a live daemon. `.#snapshot` seeds its copy with it. |
| `figaro doctor quarantine [ls] [-j]` | Every segment a corrupt frame keeps closed: a frame that fails its hash with valid frames after it. The figaro, channel, segment and byte offset, and who shares the node when it is forked history. A figaro with one refuses to open, and a crash repair leaves its node alone, rather than drop the records after the bad frame. Reads every segment, so it takes seconds on a large store. Owner only. |
| `figaro doctor quarantine release <figaro> --at <offset> [--channel <name>] [-y]` | Cut that figaro's own corrupt segment at the bad frame. Prints what goes (the frame, every record after it, every later segment of the channel) and asks for the offset typed back, or `-y`. Copies the segment and the later ones to `<state>/quarantine/<figaro>-<channel>-<segment>/` first, with a `RELEASED` note of who and when; a cut in the history also trims the figaro's translations back to it. Refuses a figaro that is awake, and a node a fork shares. Owner only. |
| `figaro doctor keys` | What `keys.toml` asked for that figaro refused. Exits nonzero on any. |
| `figaro provider login <name>` | Sign in to a model provider: its OAuth or device-code flow, else an API key sealed in hush. See [reference/providers.md](reference/providers.md#credentials). In the pager, `:login [<name>]` lists every provider's sign-in and runs the same flow in a pit: link and device code shown there, questions asked in a masked box, `x` signs out or cancels. It needs hush already running; it never asks for the vault passphrase. With `FIGARO_NO_BROWSER` set, or no display, neither opens a browser: the link is shown to open by hand. |
| `figaro provider logout <name> [-j]` | Remove what figaro holds for it (the hush credential, a sealed `api_key`). Names and keeps what figaro did not write; exits 1 while a credential remains. |
| `figaro troupe login <name>` | Sign in to a troupe's door. `[auth] scheme = "oidc-device"`: hush holds the token as `troupe:<name>`. `scheme = "api-key"`: reads the key without echo (or one line of stdin), checks it at the door, and hush seals it in `troupes/<name>.key`; with `key_file` set, it only checks that file's key. |
| `figaro troupe ls [-j]` | The troupes in `troupes/`: url, signed in or not, and `●` on the one selected. With `[peers]` or `[troupe] name` configured, also this node's `[troupe] version`, when its node certificate and intermediate end, and, per peer, the version it last showed on a handshake and any drift (asks a running daemon; never starts one). Under the figaro provider also who owns and who admitted each node, which can sign, and the nodes that left or were revoked. |
| `hush troupe-root create <name>` | Start a troupe here from the root key on stdin (never written): `root.crt` and this node's one-year intermediate. See [reference/architecture.md](reference/architecture.md#troupe-keys). |
| `hush troupe-root intermediate <name>` | Issue or renew this node's intermediate from the same root. |
| `figaro troupe keygen --out <dir> [--allow-disk]` | A node key for a credential: `node.key` (0600) in `<dir>`, a tmpfs unless `--allow-disk`; prints the public key. |
| `hush troupe-root leaf <name> --for <node-id> --public-key <hex> --door <host:port> --out <dir> [--ttl 30d]` | Issue another node a leaf from this node's intermediate for the key it made, record its join, and have the root name this node its renewer. Writes `root.crt`, `node.crt`, `joined.json` for its troupe dir. `--ttl` defaults to 7d, at most `[troupe] max_leaf_ttl`. |
| `hush troupe-root renewer <name> <node> <renewer>` | With the root, let `<renewer>`'s intermediate issue `<node>`'s certificates (a node joined through another member needs this once). |
| `hush troupe-root policy set <name> <file> --root` | Sign a troupe policy version with the root; version 1 needs it, and its `setters = [...]` names the nodes that may sign the next. |
| `hush troupe-root intermediate <name> --for <node-id> --out <dir> [--allow-disk]` | Issue another node's intermediate into `<dir>` (a tmpfs unless `--allow-disk`): `root.crt`, `intermediate.crt`, `intermediate.key`, 0600. Prints fingerprints only. |
| `figaro troupe join <name> [--door host:port] [--listen host:port] [--root-sha256 <hex>\|-y]` | Sign this node into troupe `<name>` through the member `troupes/<name>.toml` reaches (needs `join` on `troupe:<name>`). Shows the root's sha256 and asks before trusting it; writes this node's leaf and the `[troupe]`, `[door.tls]` and peercred config; restart the daemon after. See [reference/architecture.md](reference/architecture.md#troupe-membership). |
| `figaro troupe leave <name> [-y]` | The running daemon signs this node's leave with its own key and tells every member before it returns; then its troupe keys and `[troupe]`/`[door.tls]` go. Final for this node id. |
| `figaro troupe apikey mint <name> <kind:name> [--nodes <id,...\|all>] [--ttl 90d] [--static]` | Mint an api key for `service:<n>` or `user:<n>`, scoped `@troupe:<name>` (needs admin on the troupe). The key goes to stdout once; the node keeps its hash. It is good on the nodes `--nodes` names (default: the minting node alone) and is never sent to another. `--nodes all` is refused while any member is leaf-only; name the nodes instead. Keep everyday keys short, days or weeks rather than the 365-day ceiling: a member offline at a revoke admits the key until its next gossip exchange, at most an hour after it is back. `--static` prints the key and an `[[authz.api_keys]]` stanza instead. See [reference/architecture.md](reference/architecture.md#troupe-api-keys). |
| `figaro troupe apikey ls <name> [-j]` | Keys with who minted each, the nodes each is good on, and their state; every key for a troupe admin, else the ones you minted or that name you. |
| `figaro troupe apikey revoke <name> <id>` | Revoke a key (its minter, its own principal, or a troupe admin) and tell every member; its open connections close. |
| `hush troupe-root revoke <name> <node>` | Revoke a node with the root: it is out, and so is every join its intermediates signed. The running daemon tells every member and lists who heard. |
| `figaro box serve --socket <path> --workspace <dir> [--door <path>]` | Inside a box: serve it to a daemon over `--socket`. No config, no state, no daemon. `--door` is the daemon's door for this box as seen inside it; `figaro` run in the box calls back through it. See [reference/architecture.md](reference/architecture.md#boxes-internalsandboxbox). |
| `figaro box exec --socket <path> [--cwd <dir>] -- <command>` | Run one command through a served box, stream its output, exit with its code. |
| `figaro provider ls [<name>] [-j]` | Each provider's credential sources in the order figaro tries them; the first is in use. |
| `figaro models [--refresh] [-j]` | The daemon's model catalog (`@models`): every provider's models, when each was listed, and its last error. `--refresh` lists them all now. |
| `figaro update [--check\|--apply]` | Check for a newer release and print the command that upgrades this install; a nix profile entry is named as `nix profile list` names it. The installers leave a `.figaro-channel` marker beside the binary, so the channel is read rather than guessed. |
| `figaro completion install [shell]` | Install shell completion. |
| `figaro version` | Build identity: revision, exe path, Go version. Alias `v`. |

The angelus respawns on the next command after a stop, so `figaro stop` is how
you pick up a rebuilt binary. Every shell stays attended across it: the daemon
keeps `<runtime-dir>/bindings.json` current as shells attend and leave, so a
stop, an upgrade, a unit restart and a crash all hand the next angelus the
same bindings. Where a systemd user manager answers (its socket under
`$XDG_RUNTIME_DIR/systemd`, a directory this user owns and nobody else can
write), a daemon a command starts
runs in its own transient scope, `figaro-angelus-<instance>-<pid>.scope`
(`figaro-angelus-dev-...` from a dev shell), so it outlives the unit or shell
that started it; with no user manager, or with `FIGARO_NO_ANGELUS_SCOPE=1`
(every test run sets it), it stays in the caller's cgroup, detached by setsid
alone. A daemon a unit runs itself (`figaro --angelus`) is never moved. A CLI
pointed at another daemon (`FIGARO_TROUPE`
or `[cli] troupe`, naming `troupes/<name>.toml` or giving a `unix://` or `wss://`
URL) never spawns one.

A troupe file is the door as this CLI reaches it:

```toml
# ~/.config/figaro/troupes/spain.toml
url      = "wss://fig.kelliher.info"   # audience = "..." optional; default is the url
[auth]                              # optional; how a human signs in
scheme    = "oidc-device"           # or "api-key"; any other is refused
issuer    = "https://auth.kelliher.info"
client_id = "figaro-cli"
```

`figaro troupe login spain` runs the device flow and hush holds the token as
`troupe:spain`. A key the file does not define is refused when it is read.

With no troupe selected, an id on another node (`<node>/<id>`, the node
whole or 8+ characters) goes to the door of the profile that answers for that
node: figaro asks each wss profile once for its node id and keeps the answers
in `<cache-dir>/troupe-doors.json`, keyed by url, asking again when a node is
missing. The first route to a door in a process checks that it still answers
as that node, and applies the build check `FIGARO_TROUPE` gets. Profiles sharing a url are one door; `[troupe] name` speaks first,
else the first signed in. A profile's token only goes to its own url. A node
no profile reaches is refused (`no troupe profile reaches node <n>`), as is a
node two doors claim. A login the door refuses or hush lacks reads
`figaro troupe login <name>`. The topology (`l`, `L`) opens another node's
rows the same way.

A troupe that a service reaches with an api key:

```toml
# ~/.config/figaro/troupes/estate.toml
url = "wss://fig.example"
[auth]
scheme   = "api-key"
key_file = "/run/credentials/ci/figaro-key"   # optional; 0600, yours
```

Without `key_file`, `figaro troupe login estate` reads the key and hush
seals it in `troupes/estate.key`. The key is never read from argv or the
environment.

## The vault

figaro embeds hush rather than talking to the user's: its own identity
(`~/.config/figaro/hush/identity.age`), its own agent, and its own OS-keyring
entry under service `figaro`: *not* `hush`. The `hush` binary on PATH therefore
addresses a different instance and cannot repair figaro's. Alias: `figaro hush`.

**The agent's life is the daemon's.** It is figaro's own binary, re-exec'd by
hush with `HUSH_AGENT_CHILD=1`; the daemon spawns it the first time a
credential strategy needs hush (an encrypted key, an OAuth or exchanged
token) and keep-alives it from then on, and it serves nobody else. A daemon
whose keys all come from the environment or from files never starts one.
So `figaro stop` retires it, including `--force` and
including the case where no daemon answers, since an agent outlives a daemon
that was killed rather than stopped. `--keep-agent` (`-k`) keeps it, because a restart
is coming and the agent holds the unlocked identity the next daemon would
otherwise rebuild.

Which agent gets stopped is not derived from the environment: a daemon
advertises the one it owns in `<runtime-dir>/hush-agent`, and stop acts on that
claim. An external agent is never claimed and so never stopped.

**One agent can have several daemons.** `.#share-hush`, `.#snapshot` and
`.#sandbox` isolate the runtime dir and share the hush surface, so a dev daemon
and the real one hold the same agent. Each registers beside the socket
(`<hush-runtime-dir>/figaro-users/`), and stop retires the agent only when no
other registered daemon is still alive; otherwise it says who is holding it.
Registrations whose daemon has died are pruned on the way past, so the last one
out still turns the light off.

| Command | Effect |
|---|---|
| `figaro vault status` | Mode, identity path, public key, agent liveness, unlock method, and whether the saved passphrase actually decrypts the identity. Every keyring call it makes is bounded at 5s, because a locked Secret Service collection with no prompter makes `keyring.Get` block forever rather than fail. |
| `figaro vault init [--file \| --passphrase]` | Create the identity. `--file` writes 32 random bytes, base64, to `<hushconfig>/passphrase` mode 0600, points `hush.toml` at it, and asks you nothing, ever. `--passphrase` asks. |
| `figaro vault reset [--file \| --passphrase] [--yes]` | Throw the identity away and make a new one, then walk back through `figaro provider login` for every provider the old one held. |
| `figaro vault forget` | Delete the keyring entry. The next figaro command prompts. |
| `figaro vault unlock` | Prompt, verify against the identity, save to the keyring, start the agent. |
| `figaro vault lock` | Stop the agent, dropping the decrypted identity from memory. |

With neither flag, `init` and `reset` choose the file when no OS keyring
answers, since a passphrase with nowhere to be remembered is a question asked on
every command: this is the WSL case, where gnome-keyring runs but no collection
can be created headlessly. What `reset` costs you is the provider tokens that
identity encrypted and nothing else, since figaros, forms and outfits are not
encrypted with it; the old identity and token files are renamed `*.reset-<stamp>`,
not deleted.

The failure this exists for: a saved passphrase that stops decrypting makes
every command die with `incorrect passphrase`, and the unlock backend's
self-heal (clear the entry, re-prompt) cannot help if the entry keeps coming
back wrong. `vault status` names the condition and `vault forget` is the exit;
`ensureHush` prints both when it sees that error. hush ≤ v0.6.2 could *cause*
it: `Hooks.Verify` was handed the live passphrase buffer, and `identity.Unlock`
zeroes what it is given, so the verified passphrase reached the keyring as NUL
bytes. Fixed in v0.6.3: verification runs against a copy.

## Keys while streaming

| Key | Effect |
|---|---|
| Ctrl-C | Interrupt the turn. |
| Ctrl-D | Disconnect this CLI, leave the turn running. |
| Ctrl-T | Open the transcript pager. |
| Alt-m | More: names beside their other spelling, node addresses in spare columns. |
| Ctrl-O / Ctrl-I | Jumplist: back / forward through the figaros attended here. |
