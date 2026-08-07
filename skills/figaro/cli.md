# The command surface

Every verb, and what it actually does. The nine daily gestures are in
[SKILL.md](SKILL.md); this file is where you come for the rest, or for a flag's
exact meaning.

These hold across the whole CLI:

- **Flags go after the verb.** `figaro send -e -- <prompt>`, never
  `figaro -e send -- <prompt>`. The only tokens figaro reads *before* a verb
  are `--version`/`-V`, `--help`/`-h`, and the binding overrides
  `--no-bind`/`--absolute`/`-A`/`--bind`; anything else there is an unknown
  command, and gets a did-you-mean rather than a guess. The bare prompt form
  (`figaro [flags] -- <prompt>`) is the one exception, and it takes `send`'s
  flags because it *is* `send`.
- **`--` separates flags from a prompt.** Flags go before it, prompt after. A
  `--` inside the prompt body is prompt text, and nothing before the boundary
  is dropped in silence — an unrecognized token is an error.
- **Targeting** is `--id <id>` everywhere, or a positional `<id>` on most
  verbs. With neither, a verb targets the aria this shell is attended to. The
  precedence is `--id` beats `FIGARO_ARIA` beats the pid binding.
- **`-j`/`--json`** submits and exits, printing one machine-readable line on
  stdout and nothing else. It contradicts anything that streams or renders
  (`-r`, `-v`, `-o`, `-l`, `-x`, `-e`), and says so rather than dropping them.
- **`-h`/`--help`** prints that verb's help on **stdout**, exit 0 — reserved on
  every verb, so no command may claim `-h` for itself. Usage printed because
  argv was wrong goes to **stderr** with **exit 2**; exit 1 means the command
  ran and failed. `figaro help [<verb>]` is the same help by another door.

## Prompting

| Command | Effect |
|---|---|
| `figaro -- <prompt>` | Prompt the attended aria. Creates one if this shell has no binding. |
| `figaro send [flags] -- <prompt>` | The same, with the verb spelled out. Alias `qua`. |
| `figaro new [--outfit <name>] -- <prompt>` | Mint a fresh aria, bind this shell to it, prompt it. |
| `figaro new` | With no prompt and no outfit: drop this shell's binding and go home. |

`send` flags, all combinable unless noted:

| Flag | Meaning |
|---|---|
| `-f`, `--forget` | Submit and exit. Do not attach to the stream, do not interrupt on Ctrl-C. The turn keeps running in the daemon. Mints an aria if this shell has none (the id goes to stderr, or to stdout with `-j`). |
| `-e`, `--ephemeral` | Spin a throwaway in-memory aria and kill it when the turn ends. Contradicts `--id`. |
| `-O`, `--outfit <name>` | The outfit for an aria **this call creates** — with `-e`, or in a shell with no binding. Rejected against a target (`--id`, `<id>`, `<id>:<turn>`), which names an aria that already exists. Defaults to config.toml's `default_outfit`, as `new --outfit` does. Takes a value, so it does not gang: `-er -O sonn5`, not `-erO`. |
| `-r`, `--raw` | Plain text on stdout: no ANSI, no markdown. Streamed, not buffered. |
| `-o`, `--verbose` | Expand full tool inputs. Ctrl-O toggles it live. |
| `-l`, `--listen` | Open the transcript pager at startup. |
| `-v`, `--verbatim` | Dump raw wire frames as JSON, one object per line. |
| `-x`, `--exec` | Treat the reply as a bash script and run it. `-n` prints without running, `-y` skips the confirmation. |
| `-j`, `--json` | Submit, print one line `{aria_id, mode}`, exit. Contradicts `-r`/`-v`/`-o`/`-l`/`-x`/`-e`. |

Persistence (`-e`) and formatting (`-r`) are orthogonal. The workhorse for
scripts is `figaro send -er -- <prompt>`: one shot, isolated, pipe clean.

Timing is not a flag. A prompt that arrives while a turn is running joins it as
a steering aside; a prompt that arrives when nothing is running opens a turn.
The classification happens where the queue is drained, and nowhere else.

## The queue

**Every drain coalesces the contiguous run of queued prompts into one message;
a queued `set` or `fork` is a barrier.** Four chained sends against an idle
aria are one reply and then ONE combined question — not four turns to sit
through — and the same is true of messages queued behind a turn you then
interrupt. The messages are separated by a blank line, so they stay separate
lines on screen and separate messages to the model. A `set` queued between two
of them still applies in order, because folding a prompt in front of it would
answer that prompt against a chalkboard it was never written against.

A single submit is untouched: one message in, one message out.

Prompts that arrive while the aria is busy wait in its **queue**. They are
addressed by an id from the listing, paired with the **epoch** that id was read
in — ids restart whenever the agent is rebuilt (a daemon restart, a
dormant→attach), so every mutation re-reads first and hands the epoch back. A
request from a previous generation is refused as `stale` rather than resolved
against whatever holds that number now.

| Command | Effect |
|---|---|
| `figaro queue [--id <aria>] [-j]` | List: id, state, age, text. |
| `figaro queue rm <id>...` | Drop those messages. |
| `figaro queue rm --all` | Drop all of them. |
| `figaro queue edit <id> -- <text>` | Rewrite one. |

To ADD to the queue, `send` — a queued message is just a prompt that arrived
while the aria was busy, so there is no separate create verb. The sub-verb owns
the positional slot, so another aria is `--id`, never a bare id.

A refusal is an **answer**, not a crash. The agent declines to delete a message
it has already committed, and says which: `committing` (lifted by the drain
loop this instant), `committed` (already part of the conversation), `merged` (an
interrupt folded it into another queued message — the survivor's id is given),
`stale`, `unknown`, `closed`. Exit is 0 when every id was applied, 1 when any
was refused, 2 for misuse.

## Hanging up

Two verbs, differing in one thing: what becomes of the queue.

| Command | Effect |
|---|---|
| `figaro hup [<id>] [-j]` | Stop the turn, **keep** queued messages. |
| `figaro hup -d [<id>] [-j]` | Stop the turn and **discard** them. |
| `figaro cut [<id>] [-j]` | Shorthand for `hup -d`. |

Both forms **return** the queued messages — listed on stdout, or as JSON with
`-j` — so discarding is not losing.

`hup` is the everyday one, and what it leaves behind is governed by the rule
below.

`cut` hands back what it dropped, verbatim, one entry per message as you typed
it, with the chalkboard input each carried:

    figaro cut -j > lost.json

Clearing does not need a turn to be running. Neither verb touches a queued
`set` or `fork`: they drop questions, not someone else's control events.

In the transcript pager, **`H`** is `hup` from the keyboard and **`X`** is
`hup -d`: both stop the turn and keep watching. They are the third thing you
can do to a running turn — `Ctrl-C` stops it and exits, `Ctrl-D` exits and lets
it run on, `H`/`X` stop it and stay. What `X` drops is printed into the status
line and reprinted to the shell when you leave the pager, so the text survives
even though its place in the queue does not.

The wire behind all of this — the interrupt's queue disposition, the
`(epoch, id)` identity, and the closed set of refusal reasons — is
[reference/ui-stream.md](reference/ui-stream.md).

## Moving around

| Command | Effect |
|---|---|
| `figaro ls [<id>]` | List arias, scoped to where you are attended. Alias `list`. |
| `figaro ls -H` | Home view: every top-level aria, without unbinding you. (`-h` is help, on every verb.) |
| `figaro ls -g` | Home plus the null root and outfit anchors. |
| `figaro ls -a` / `-n N` | Remove the 10-row cap, or set it. Mutually exclusive. |
| `figaro attend <id>` | Bind this shell to an aria. Alias `at`. |
| `figaro attend <id>:<turn>` | Bind with a pending fork point: the next bare prompt forks there. `<id>.<lt>` names an LT instead of a turn. |
| `figaro attend null` | Go home. There is no `detach` verb. |
| `figaro show [<id>]` | Render history — the same rows `listen` draws. `-n N` last N turns, `-a` all, `-o` block addresses and timestamps, `-v` raw IR, `-l` no markdown, `-j` JSON. |
| `figaro status [<id>]` | One aria in focus: provider, model, context, cost. `-m` adds cwd, outfit, fork origin. |
| `figaro listen [<id>]` | Attach to the live stream without prompting. Ctrl-D detaches, the turn survives. |
| `figaro hup [<id>] [-d]` | Hang up: stop the turn; `-d` also discards the queue (see The queue). |
| `figaro cut [<id>]` | Shorthand for `hup -d`; `-j` returns the messages. |
| `figaro queue [rm\|edit]` | Read, edit and delete what has not been answered yet. |

`ls` columns: ARIA (mantra, with `●` this shell, `▸` running, `○` idle), ID,
OUTFIT, VER, FORK, AGE, MSGS, CTX, CWD. While your own turn is in flight your
row shows `▸`, not `●`, so identify yourself from the header instead.

## Branching

| Command | Effect |
|---|---|
| `figaro fork` | Branch the attended aria at its head. |
| `figaro fork <id>:12` | Interior fork: history through turn 11 is shared, the original suffix continues, a fresh empty alternative diverges. |
| `figaro fork [flags] -- <prompt>` | Branch and immediately prompt the new branch. Takes `send`'s flags; `-e` is rejected. |
| `figaro fork --stay` | Branch without moving this shell. |
| `figaro promote <id> [levels]` | Make a trunk the canonical line through its ancestors. Pure relabeling. |
| `figaro kill <id>` | Remove a trunk and its subtree. `-r` is required if it has live branches. |
| `figaro export [<id>] [-o <f>]` | Write an aria to a portable file: outfit, chalkboard, every message. |
| `figaro import <file>` | Restore one into THIS store as a new conversation. `-` reads stdin. |

The model behind these, including what freezes and which child keeps the id, is
[reference/trunks.md](reference/trunks.md).

**Moving an aria between stores** — out of a dev shell, onto another machine —
is `export` then `import`. They carry CONTENT, not identity: node ids, fork
bases and LTs belong to the store an aria is in, so the destination mints its
own and an import can never collide with what is already there. Three things do
not travel: the aria's id (a trunk id is unique per store — the old one is
reported as provenance), branches, and the provider translation caches, so the
first turn after an import re-translates. Preserving all three needs a byte-level
graft, which is designed in [proposals/aria-graft.md](../../proposals/aria-graft.md)
and deliberately not built: an import that goes wrong refuses, while a graft
that goes wrong yields a store that looks fine.

## State

| Command | Effect |
|---|---|
| `figaro state [<id>]` | The folded chalkboard snapshot. `-j` for JSON. Alias `chalkboard`. |
| `figaro set [<id>] <key> <value>` | Patch one key with no model round trip. |
| `figaro unset [<id>] <key>...` | Remove keys. |
| `figaro outfit <name>[,<name>...]` | Apply named outfits additively, folded left to right (later wins). `--list` to see them. |
| `figaro gc [--dry-run]` | Collect outfit stumps nothing is using. One stump exists per outfit VERSION; killing an aria collects its stump when it was the last child, so `gc` sweeps the versions that predate that. Content-addressed, so a collected stump is re-minted identically by the next aria that wants it. |
| `figaro outfit --tree [<name>]` | Draw an outfit's layer closure and apply nothing — green where a layer resolves, red where it does not. Reads the config dir directly, so it needs no aria and no daemon. Exits non-zero when the closure has a gap. |

Setting a key is a real event in the conversation: on the tic where a
non-`system` key changes, the agent sees a `<system-reminder>` naming it. Keys
under `system.` are hidden from the agent and read directly by the harness.

## Daemon and tools

| Command | Effect |
|---|---|
| `figaro stop` | Shut down the angelus. Alias `rest`. `-f` to SIGKILL, `-k` to persist pid bindings. |
| `figaro doctor gc` | Remove dead store channels. `-n` to report without touching. Requires the daemon stopped. |
| `figaro doctor schema` | Report per-channel format versions. |
| `figaro doctor mem [-j]` | What the daemon is holding: live/resident arias, IR cache, endpoints, attached clients, heap. See [reference/reclamation.md](reference/reclamation.md). |
| `figaro login <provider>` | OAuth login. |
| `figaro models` | List available provider models. |
| `figaro update [--check\|--apply]` | Check for a newer release. |
| `figaro completion install [shell]` | Install shell completion. |
| `figaro version` | Build identity: revision, exe path, Go version. Alias `v`. |

The angelus respawns on the next command after a stop, so `figaro stop` is how
you pick up a rebuilt binary.

## The vault

figaro embeds hush rather than talking to the user's: its own identity
(`~/.config/figaro/hush/identity.age`), its own agent, and its own OS-keyring
entry under service `figaro` — *not* `hush`. The `hush` binary on PATH therefore
addresses a different instance and cannot repair figaro's. Alias: `figaro hush`.

| Command | Effect |
|---|---|
| `figaro vault status` | Mode, identity path, public key, agent liveness, unlock method, and whether the saved passphrase actually decrypts the identity. |
| `figaro vault forget` | Delete the keyring entry. The next figaro command prompts. |
| `figaro vault unlock` | Prompt, verify against the identity, save to the keyring, start the agent. |
| `figaro vault lock` | Stop the agent, dropping the decrypted identity from memory. |

The failure this exists for: a saved passphrase that stops decrypting makes
every command die with `incorrect passphrase`, and the unlock backend's
self-heal (clear the entry, re-prompt) cannot help if the entry keeps coming
back wrong. `vault status` names the condition and `vault forget` is the exit;
`ensureHush` prints both when it sees that error. hush ≤ v0.6.2 could *cause*
it: `Hooks.Verify` was handed the live passphrase buffer, and `identity.Unlock`
zeroes what it is given, so the verified passphrase reached the keyring as NUL
bytes. Fixed in v0.6.3 — verification runs against a copy.

## Keys while streaming

| Key | Effect |
|---|---|
| Ctrl-C | Interrupt the turn. |
| Ctrl-D | Disconnect this CLI, leave the turn running. |
| Ctrl-T | Open the transcript pager. |
| Ctrl-O | Toggle verbose tool expansion. |
