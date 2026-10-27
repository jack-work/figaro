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
  verbs. With neither, a verb targets the aria this shell is attended to. The
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
| `figaro -- <prompt>` | Prompt the attended aria. Creates one if this shell has no binding. |
| `figaro send [flags] -- <prompt>` | The same, with the verb spelled out. Alias `qua`. |
| `figaro new [-O <names>] [-S <k=v>] -- <prompt>` | Mint a fresh aria, bind this shell to it, prompt it. |
| `figaro new -C [<@form>]` | Mint the aria and CAST it in one gesture. With a role named, `-O`/`-S` dress the aria; with none, they mint the role (`new -CO reviewer`, `new -CS name=x`). When the call MINTS a role, the shell ends up attending that role (`--stay` opts out); a role redirects turn-shaped verbs to its holder, so prompts still reach the aria. A figaro minted but not cast is reported as that partial, with its id. |
| `figaro new` | With no prompt: mint on the default outfit and attend it, no turn. `new` always mints; `figaro attend null` is how you go home. |
| `figaro decide [-C <a,b>] -- <question>` | Ask a decision model (jev or luna) a yes/no or multiple-choice question over the attended aria's conversation. Reads only; prints the scored answer. See [reference/decisions.md](reference/decisions.md). |

`send` flags, all combinable unless noted:

| Flag | Meaning |
|---|---|
| `-f`, `--forget` | Submit and exit. Do not attach to the stream, do not interrupt on Ctrl-C. The turn keeps running in the daemon. Mints an aria if this shell has none (the id goes to stderr, or to stdout with `-j`). |
| `-O`, `--outfit <names>` | Dress the aria in OUTFIT NAMES. On one this call creates it is the birth outfit; on one that already exists it is folded onto the form **in the same call as the prompt**, so that turn is answered wearing it. Repeats append (`-O a -O b` = `-O a,b`). Bundles like any value-taking short: `-rO sonn5` or `-rOsonn5`. Defaults to `default_outfit`. See [reference/outfits.md](reference/outfits.md). |
| `-S`, `--set <k=v>` | Form keys, the other axis: `k=v` or a JSON literal, comma-separated. Folded AFTER `-O`, so your key beats the outfit's. |
| `-D`, `--delete <paths>` | Form key paths to remove, comma-separated. Folded last. |
| `-r`, `--raw` | Plain text on stdout: no ANSI, no markdown. Streamed, not buffered. |
| `-o`, `--verbose` | Start with more on: names beside their other spelling, node addresses. `M-m` toggles it live. |
| `-l`, `--listen` | Open the transcript pager at startup. |
| `-v`, `--verbatim` | Dump raw wire frames as JSON, one object per line. |
| `-x`, `--exec` | Treat the reply as a bash script and run it. `-n` prints without running, `-y` skips the confirmation. |
| `-j`, `--json` | Submit, print one line `{aria_id, mode}`, exit. Contradicts `-r`/`-v`/`-o`/`-l`/`-x`. |

Every aria persists. A one-off is an ordinary send that mints and states its
own lifetime: `figaro send -A -f -S system.ttl=1h -- <prompt>`, or `-r` in
place of `-f` to stream plain text into a pipe. `-A` ignores this shell's
binding, so the send mints instead of steering the attended aria. Inside an
aria's own shell `FIGARO_ARIA` outranks `-A` and the send reaches you; mint
with `new` there ([subagents.md](subagents.md)).

Timing is not a flag. A prompt that arrives while a turn is running joins it as
a steering aside; a prompt that arrives when nothing is running opens a turn.
The classification happens where the queue is drained, and nowhere else.

## The queue

**Every drain coalesces the contiguous run of queued prompts into one message;
a queued `set` or `fork` is a barrier.** Four chained sends against an idle
aria are one reply and then ONE combined question: not four turns to sit
through, and the same is true of messages queued behind a turn you then
interrupt. The messages are separated by a blank line, so they stay separate
lines on screen and separate messages to the model. A `set` queued between two
of them still applies in order, because folding a prompt in front of it would
answer that prompt against a form it was never written against.

A single submit is untouched: one message in, one message out.

Prompts that arrive while the aria is busy wait in its **queue**. They are
addressed by an id from the listing, paired with the **epoch** that id was read
in: ids restart whenever the agent is rebuilt (a daemon restart, a
dormant→attach), so every mutation re-reads first and hands the epoch back. A
request from a previous generation is refused as `stale` rather than resolved
against whatever holds that number now.

| Command | Effect |
|---|---|
| `figaro queue [--id <aria>] [-j]` | List: id, state, age, text. |
| `figaro queue rm <id>...` | Drop those messages. |
| `figaro queue rm --all` | Drop all of them. |
| `figaro queue edit <id> -- <text>` | Rewrite one. |

To ADD to the queue, `send`, a queued message is just a prompt that arrived
while the aria was busy, so there is no separate create verb. The sub-verb owns
the positional slot, so another aria is `--id`, never a bare id.

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
| `figaro ls [<id>]` | List arias. Attended, the default scope is you and everything forked below you; detached it is home. `<id>` names another subtree, `/` is home. Alias `list`. |
| `figaro ls ^` / `^2` / `^^` | Climb N layers: your parent's subtree, you still in it. Past the root it clamps to the top-level aria and prints a note. |
| `figaro ls -H` | Home view: every top-level aria, without unbinding you. (`-h` is help, on every verb.) |
| `figaro lsh` | `ls -H`, spelled shorter. Same flags. |
| `figaro ls -g` | Home plus the null root and outfit anchors. |
| `figaro ls -a` / `-n N` | Remove the 10-row cap, or set it. Mutually exclusive. |
| `figaro attend <id>` | Bind this shell to an aria. Alias `at`. |
| `figaro attend <id>:<turn>` | Bind with a pending fork point: the next bare prompt forks there. `<id>.<lt>` names an LT instead of a turn. |
| `figaro attend null` | Go home. There is no `detach` verb. |
| `figaro show [<id>]` | Render history: the same rows `listen` draws. `-n N` last N turns, `-a` all, `-o` block addresses and timestamps, `-v` raw IR, `-l` no markdown, `-j` JSON. |
| `figaro status [<id>]` | One node in focus, whichever species it is. A FIGARO: provider, model, context, cost. A FORM: its name, version, key count, and that it is not cast. A ROLE: the same plus `target-aria` and the state of the aria it points at (live, dormant, or missing). `-m` adds cwd/outfit/fork origin for a figaro, and parent plus the keys held for a form. `-j` carries `species`. |
| `figaro listen [<id>]` | Attach to the live stream without prompting. Ctrl-D detaches, the turn survives. |
| `figaro hup [<id>] [-d]` | Hang up: stop the turn; `-d` also discards the queue (see The queue). |
| `figaro cut [<id>]` | Shorthand for `hup -d`; `-j` returns the messages. |
| `figaro queue [rm\|edit]` | Read, edit and delete what has not been answered yet. |

`ls` columns: ARIA (mantra, with `●` this shell, `▸` running, `○` idle), ID
(the aria's word handle), OUTFIT, VER, FORK, AGE, MSGS, CTX, CWD. While your own turn is in flight your
row shows `▸`, not `●`, so identify yourself from the header instead.

`attend` binds the **terminal**, not the process that typed it: on unix the key
is the session leader, so subshells, pipelines, `$(...)`, `timeout figaro …`
and a shell-prompt segment all resolve to the same aria. A script run from an
attended shell therefore speaks to that shell's aria: it cannot move the
attendance, only use it. Windows has no POSIX session and keys on the parent.
Override with `--no-bind`/`-A` (ignore the binding) or `--bind` (use it where
the no-TTY rule would switch writes off); both go before the verb.

### Naming an aria

Every `<id>` above takes any of these, and all of them name the same aria:

| Form | Example |
|---|---|
| the full id | `a5647bfbc216b8a09ffea9586393c289` (old arias: `d9a9cebf`) |
| its word handle | `nursery-lioness` |
| the handle's stems, 3+ letters a word | `nur-lio` |
| any unique prefix of the handle, 3+ letters | `nurs` |
| any unique hex prefix, 4+ characters | `a5647` |
| either, on another node | `spain2/nursery-lioness` |
| a form | `@` before any of them |

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
pager `y` copies the name shown (the attended aria's, or a listing row's) and
`M-y` the full id.

## Branching

| Command | Effect |
|---|---|
| `figaro fork` | Branch the attended aria at its head. |
| `figaro fork <id>:12` | Interior fork: history through turn 11 is shared, the original suffix continues, a fresh empty alternative diverges. |
| `figaro fork [flags] -- <prompt>` | Branch and immediately prompt the new branch. Takes `send`'s flags. |
| `figaro fork --stay` | Branch without moving this shell. |
| `figaro promote <id> [levels]` | Raise an aria in the tree `ls` draws; it takes its parent's place. Presentation only. |
| `figaro succeed <@role> [--turn <n> \| --successor <aria>] [--title <t>] [--ttl <d>] [--abdication retire\|linger] -- <prompt>` | Hand a role to a successor: fork the aria it targets at `--turn` (default 2) or take `--successor`, demote the predecessor beneath it, prompt and cast the successor, then retire the predecessor with a ttl (default 10m; `0` kills it). `linger` leaves it promptable for an overlapping handover. Not atomic: a failure names the commands that finish the job. `:succeed` in the pager follows the successor unless `--stay`. See [reference/forms.md](reference/forms.md#roles). |
| `figaro demote <id> [--under <child>]` | Sink an aria to a leaf: a child takes its place, the other children move under that child, and a kill then takes the aria alone. Presentation only. |
| `figaro kill <id> [-r] [--when <d> \| --now]` | Stop a trunk's agent and sessions, then reap it (and with `-r` its branches, which a kill without `-r` refuses) until its grace runs out: `--when` (`90m`, `6h`, `2d`, `1w`), else `[memory] kill_grace_hours`, three days. `--now` or `--when 0` deletes at once. Killing a reaped aria moves its group's purge to the new grace. |
| `figaro reaped [ls \| restore <id> [--keep] \| purge <id>]` | What `kill` and the ttl sweep soft-deleted, when each group is purged, and the way back. |
| `figaro retire <id> [--ttl <d>] [-O ..] [-S ..] [--undo]` | Stop an aria taking prompts. Reads and forks still work; a fork is not retired. The outfits and keys land in the same write; `--ttl 1h` reaps it an hour from now unless something holds it. |
| `figaro export [<id>] [-o <f>]` | Write an aria to a portable file: outfit, form, every message. |
| `figaro import <file>` | Restore one into THIS store as a new conversation. `-` reads stdin. |

The model behind these, including what freezes and which child keeps the id, is
[reference/trunks.md](reference/trunks.md).

**Moving an aria between stores**: out of a dev shell, onto another machine -
is `export` then `import`. They carry CONTENT, not identity: node ids, fork
bases and LTs belong to the store an aria is in, so the destination mints its
own and an import can never collide with what is already there. Three things do
not travel: the aria's id (a trunk id is unique per store: the old one is
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
| `figaro toggle [--id <id>] <key>` | Flip a true/false key. Absent on the aria, it reads the default outfit's value, else false. Refuses any other value. |
| `figaro form set <key> <value>` | The same, from the form family. `form set a=1,b=2` takes the `-S` grammar instead. |
| `figaro form delete <paths>` | Remove key paths, comma-separated (the `-D` grammar). `unset` is its older name. |
| `figaro form help <topic>` | A help page, from the form family's third position. |
| `figaro state outfit <names>` | Fold outfits onto this aria now: additive, nothing removed, a `<system-reminder>` for what changed. Takes NAMES only: keys are the other axis (`form set`). The names travel as names and the daemon resolves them once, at its API boundary, so the same `-O` means the same thing at birth and on a live aria. `--list` for what is on disk. See [reference/outfits.md](reference/outfits.md). |
| `figaro gc [--dry-run]` | Collect outfit versions nothing is using. One version exists per outfit content hash; killing an aria collects its version when it was the last child, so `gc` sweeps the versions that predate that. Content-addressed, so a collected version is re-minted identically by the next aria that wants it. |
| `figaro form listen [<id>]` | Watch the form as a live JSON tree, in the alt screen. The client keeps its OWN copy and applies the patches the server broadcasts (`form.delta`), so it is the same tree and the same algebra on both ends. `j`/`k` move, `enter` expands, `y` yanks, `e`/`d` page. |
| `figaro state outfit --tree [<spec>]` | Draw the layer closure and apply nothing: green where a layer resolves, red where it does not. The angelus resolves it: the outfits directory is the server's state, so a client asks rather than reading it. Exits non-zero when the closure has a gap. |

Setting a key is a real event in the conversation: on the tic where a
non-`system` key changes, the agent sees a `<system-reminder>` naming it. Keys
under `system.` are hidden from the agent and read directly by the harness.

**Lifetimes.** `figaro set --id <id> system.ttl 30d` gives a node a lifetime:
the daemon's reclamation sweep deletes that node and its subtree once the
lifetime is spent. `-S system.ttl=1h` states one at birth, on `new`, `fork`, or
a `send` that mints, and an outfit's `[system] ttl` does the same for every
aria dressed in it (the shared default form keeps none of its own). It is counted from when the node was CREATED, not from its
last turn, and a fork inherits the key with its own creation time. Go duration
syntax plus `d` and `w`; `0`, an empty value or `unset system.ttl` means no
lifetime and nothing is ever deleted.

A spent lifetime does not delete a node in use. `system.keepalive` (same
syntax; default one day, `[memory] keepalive_minutes`) keeps a node whose
lifetime is spent while it was used within that window. Use is a successful
call that names the aria: a prompt, a `set`, a read or `show`, attend, bind,
listen, a fork from it, a queue read. `ls`, the tree view, the `L` pit, status
and `doctor ttl` are not use, and neither is a call that was refused. Records
nobody asked for (translation catch-up, study marks) do not count either: a
node is measured from its last use, and from its newest record only until it
is first used under 0.50. `system.keepalive=0` gives reaping at the deadline.
The sweep runs on `[memory] sweep_interval_seconds` (two minutes by default)
and also passes over a node when anything its delete would take (the node and
its branches) has a live agent, a bound shell, a role pointing at it, or a use
within its own keepalive. A retired aria (`figaro retire`) is held only by a
keepalive it states itself, so `retire --ttl 1h` really ends it in an hour.

What the sweep takes, it first reaps rather than deletes: the node and
everything its delete would take get `system.reaped` (naming the root), leave
`ls`, refuse prompts, and are indexed in the catalog `@reaped`
(`figaro form show @reaped`). `[memory] reaped_grace_hours` later (seven days by
default; `0` deletes at once) the sweep purges the group, unless it has a live
agent, a bound shell or a role pointing in. `figaro reaped` lists the groups,
`figaro reaped restore <id>` brings one back (the keepalive then holds it a
day; `--keep` also unsets its ttl), and `figaro reaped purge <id>` deletes it
now. A fork of a reaped aria is not reaped.

`figaro kill` reaps the same way, but the group carries its own deadline
(`system.reaped_until`, the kill time plus `--when` or `[memory]
kill_grace_hours`), and at that deadline it is purged whatever holds it: a kill
is a delete deferred, not a request. A sweep-reaped group keeps
`reaped_grace_hours`; killing one gives it the kill's deadline.

`ls`, the tree view and the pager's `L` mark a row `[reaps in 3h]` inside the
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
| `figaro stop` | Shut down the angelus and retire its hush agent (see The vault). Alias `rest`. `-f` to SIGKILL, `-k` when a restart is coming. |
| `figaro doctor gc` | Remove dead store channels. `-n` to report without touching. Requires the daemon stopped. |
| `figaro doctor schema` | Report per-channel format versions. |
| `figaro doctor mem [-j]` | What the daemon is holding: live/resident arias, IR cache, endpoints, attached clients, heap. See [contributing/reclamation.md](contributing/reclamation.md). |
| `figaro doctor ttl [-j]` | Every node that states a lifetime (`system.ttl`), when each one ends, which a keepalive still holds, and which are due. Reads the sidecars, so it answers while the daemon runs. |
| `figaro keys [init\|actions\|bind\|unbind\|reset]` | The pager's keybindings, by layer, and `keys.toml` behind them. `init` starts one from the shipped recipes. See [reference/keys.md](reference/keys.md). |
| `figaro doctor keys` | What `keys.toml` asked for that figaro refused. Exits nonzero on any. |
| `figaro provider login <name>` | Sign in to a model provider: its OAuth or device-code flow, else an API key sealed in hush. See [reference/providers.md](reference/providers.md#credentials). |
| `figaro provider logout <name> [-j]` | Remove what figaro holds for it (the hush credential, a sealed `api_key`). Names and keeps what figaro did not write; exits 1 while a credential remains. |
| `figaro troupe login <name>` | Sign in to a troupe's door. `[auth] scheme = "oidc-device"`: hush holds the token as `troupe:<name>`. `scheme = "api-key"`: reads the key without echo (or one line of stdin), checks it at the door, and hush seals it in `troupes/<name>.key`; with `key_file` set, it only checks that file's key. |
| `figaro troupe ls [-j]` | The troupes in `troupes/`: url, signed in or not, and `●` on the one selected. With `[peers]` or `[troupe] name` configured, also this node's `[troupe] version`, when its node certificate and intermediate end, and, per peer, the version it last showed on a handshake and any drift (asks a running daemon; never starts one). Under the figaro provider also who owns and who admitted each node, which can sign, and the nodes that left or were revoked. |
| `op read <ref> \| figaro troupe create <name>` | Start a troupe here from the root key on stdin (never written): `root.crt` and this node's one-year intermediate. See [reference/architecture.md](reference/architecture.md#troupe-keys). |
| `op read <ref> \| figaro troupe intermediate <name>` | Issue or renew this node's intermediate from the same root. |
| `figaro troupe keygen --out <dir> [--allow-disk]` | A node key for a credential: `node.key` (0600) in `<dir>`, a tmpfs unless `--allow-disk`; prints the public key. |
| `op read <ref> \| figaro troupe leaf <name> --for <node-id> --public-key <hex> --door <host:port> --out <dir>` | Issue another node a leaf from this node's intermediate for the key it made, record its join, and have the root name this node its renewer. Writes `root.crt`, `node.crt`, `joined.json` for its troupe dir. |
| `op read <ref> \| figaro troupe renewer <name> <node> <renewer>` | With the root, let `<renewer>`'s intermediate issue `<node>`'s certificates (a node joined through another member needs this once). |
| `op read <ref> \| figaro troupe policy set <name> <file> --root` | Sign a troupe policy version with the root; version 1 needs it, and its `setters = [...]` names the nodes that may sign the next. |
| `op read <ref> \| figaro troupe intermediate <name> --for <node-id> --out <dir> [--allow-disk]` | Issue another node's intermediate into `<dir>` (a tmpfs unless `--allow-disk`): `root.crt`, `intermediate.crt`, `intermediate.key`, 0600. Prints fingerprints only. |
| `figaro troupe join <name> [--door host:port] [--listen host:port] [--root-sha256 <hex>\|-y]` | Sign this node into troupe `<name>` through the member `troupes/<name>.toml` reaches (needs `join` on `troupe:<name>`). Shows the root's sha256 and asks before trusting it; writes this node's leaf and the `[troupe]`, `[door.tls]` and peercred config; restart the daemon after. See [reference/architecture.md](reference/architecture.md#troupe-membership). |
| `figaro troupe leave <name> [-y]` | The running daemon signs this node's leave with its own key and tells every member before it returns; then its troupe keys and `[troupe]`/`[door.tls]` go. Final for this node id. |
| `figaro troupe apikey mint <name> <kind:name> [--nodes <id,...\|all>] [--ttl 90d] [--static]` | Mint an api key for `service:<n>` or `user:<n>`, scoped `@troupe:<name>` (needs admin on the troupe). The key goes to stdout once; the node keeps its hash. It is good on the nodes `--nodes` names (default: the minting node alone) and is never sent to another. `--nodes all` is refused while any member is leaf-only; name the nodes instead. Keep everyday keys short, days or weeks rather than the 365-day ceiling: a member offline at a revoke admits the key until its next gossip exchange, at most an hour after it is back. `--static` prints the key and an `[[authz.api_keys]]` stanza instead. See [reference/architecture.md](reference/architecture.md#troupe-api-keys). |
| `figaro troupe apikey ls <name> [-j]` | Keys with who minted each, the nodes each is good on, and their state; every key for a troupe admin, else the ones you minted or that name you. |
| `figaro troupe apikey revoke <name> <id>` | Revoke a key (its minter, its own principal, or a troupe admin) and tell every member; its open connections close. |
| `op read <ref> \| figaro troupe revoke <name> <node>` | Revoke a node with the root: it is out, and so is every join its intermediates signed. The running daemon tells every member and lists who heard. |
| `figaro box serve --socket <path> --workspace <dir> [--door <path>]` | Inside a box: serve it to a daemon over `--socket`. No config, no state, no daemon. `--door` is the daemon's door for this box as seen inside it; `figaro` run in the box calls back through it. See [reference/architecture.md](reference/architecture.md#boxes-internalsandboxbox). |
| `figaro box exec --socket <path> [--cwd <dir>] -- <command>` | Run one command through a served box, stream its output, exit with its code. |
| `figaro provider ls [<name>] [-j]` | Each provider's credential sources in the order figaro tries them; the first is in use. |
| `figaro models [--refresh] [-j]` | The daemon's model catalog (`@models`): every provider's models, when each was listed, and its last error. `--refresh` lists them all now. |
| `figaro update [--check\|--apply]` | Check for a newer release. |
| `figaro completion install [shell]` | Install shell completion. |
| `figaro version` | Build identity: revision, exe path, Go version. Alias `v`. |

The angelus respawns on the next command after a stop, so `figaro stop` is how
you pick up a rebuilt binary. A CLI pointed at another daemon (`FIGARO_TROUPE`
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
that was killed rather than stopped. `--keep-pids` keeps it, because a restart
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
identity encrypted and nothing else, since arias, forms and outfits are not
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
| Ctrl-O / Ctrl-I | Jumplist: back / forward through the arias attended here. |
