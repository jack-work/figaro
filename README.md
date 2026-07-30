# Figaro

*Largo al factotum della citta.*

A Go CLI coding agent. One binary: CLI, supervisor daemon, agent runtime. JSON-RPC over unix sockets.

## Install

```bash
nix profile install github:jack-work/figaro
# or
go install github.com/jack-work/figaro/cmd/figaro@latest
```

Also reachable as `fig` (the Nix package installs the symlink; for `go install`, add one manually).

Config lives at `~/.config/figaro/`.

## First run

```bash
figaro login copilot        # or: figaro login anthropic
figaro -- buongiorno
```

The first prompt triggers a setup wizard (provider, model, loadout). After that, `figaro --` is all you need.

Run `figaro -- :skills.howto!` to start the interactive tutorial (the howto skill walks you through arias, forking, the chalkboard, and loadouts in character).

### Copilot models

The `copilot` provider routes models by the capability advertised in the
Copilot catalog. Claude-compatible models use the Anthropic Messages
transport; Responses-capable models such as `gpt-5.6-terra` use Figaro's
native WebSocket Responses transport. Figaro does not start a Copilot CLI
process.

```bash
figaro models
```

For noninteractive use, Copilot credentials are read in this order:
`COPILOT_GITHUB_TOKEN`, `GH_TOKEN`, then `GITHUB_TOKEN`.

Choose a catalog model in a loadout:

```toml
[system]
provider = "copilot"
model = "gpt-5.6-terra"
context_tier = "long_context"
thinking_effort = "high"
reasoning_context = "all_turns"
reasoning_summary = "auto"
verbosity = "low"
max_tokens = 16000
```

Responses settings can change between turns on a live aria:

```bash
figaro set system.model '"gpt-5.6-luna"'
figaro set system.context_tier '"default"'
figaro set system.reasoning_context '"current_turn"'
figaro set system.reasoning_summary '"auto"'
figaro set system.max_context_tokens 120000
figaro set system.parallel_tool_calls false
figaro set system.temperature 0.4
```

`system.context_tier` selects the catalog's default or long-context replay
budget; `system.max_context_tokens` can impose a smaller cap. Figaro rejects
a turn that would exceed that budget rather than dropping cached history.
`system.reasoning_context` maps to the Responses API's `auto`,
`current_turn`, or `all_turns` mode. `system.reasoning_summary` accepts
`"auto"`, `"concise"`, or `"detailed"` and requests a readable reasoning
summary; it does not expose raw private chain-of-thought.
`system.temperature` and `system.top_p` are mutually exclusive. A model
switch starts a new Responses cache lineage so opaque reasoning is never
replayed under a different model.

For headless or container use, Copilot accepts credentials in this order:
`COPILOT_GITHUB_TOKEN`, `GH_TOKEN`, then `GITHUB_TOKEN`.

### Context accounting

`figaro status` and the `CTX` column in `figaro list` report **context used**:
the last turn's prompt plus that turn's output, i.e.

```
input_tokens + cache_read_input_tokens + cache_creation_input_tokens + output_tokens
```

All three input buckets count — with prompt caching on (the default) most of
the prompt comes back as a cache read, so summing only `input_tokens` would
under-report a long aria by orders of magnitude. Messages appended after the
last metered turn are estimated at chars/4 and the figure is prefixed `~`.

The window it is measured against comes from the provider (Anthropic reports
`max_input_tokens` on `/v1/models`; `figaro models` shows the same numbers).
`system.max_context_tokens` overrides it in either direction:

```bash
figaro set system.max_context_tokens 200000   # pin the window
figaro unset system.max_context_tokens        # back to the provider's number
```

Unknown models report no window rather than a guessed one, and status then
prints a bare token count.

## Core concepts

- **Arias**: persistent conversations, append-only IR log, fork-tree storage via [figwal](https://github.com/jack-work/figwal).
- **Forking**: branch at a turn boundary; both sides share the canonical IR prefix. `attend` is your `cd`.
- **Chalkboard**: per-aria key-value state, travels as patches, surfaces as system reminders.
- **Loadouts**: TOML profiles (provider, model, credo, skills) inherited by new arias.
- **Tools**: bash, read, write, edit, process. Parallel dispatch.
- **Providers**: Anthropic (direct + SDK), GitHub Copilot. Registry-driven, no switches.

## Commands

```
figaro -- <prompt>              prompt the bound aria
figaro send -r -- <prompt>      raw output (pipe-friendly)
figaro list                     show arias
figaro attend <id>              bind to an aria
figaro fork                     branch at head
figaro fork <id>:12 -- <p>      branch at a turn and prompt the branch
figaro show <id> -n 5           last 5 turns
figaro listen <id>              follow an aria without prompting it
figaro set <key> <value>        patch chalkboard state
figaro status                   current aria info
figaro --help                   full command list
```

## Alternate frontends

Each aria exposes a semantic, turn-shaped UI stream over local NDJSON JSON-RPC:
`figaro.read` pulls `aria.Page` snapshots/pages and `figaro.aria` pushes the same
shape as nodes change; `turn.done` carries completion/idle control state. The
stock `send`, `listen`, inline view, and transcript pager are clients of that
stream. See [`skills/figaro/reference/ui-stream.md`](skills/figaro/reference/ui-stream.md) and
[`skills/figaro/reference/turns.md`](skills/figaro/reference/turns.md).

The socket surface is currently trusted-local and revision-coupled, not yet a
stable public API. Protocol versioning, capability negotiation, public client
packages/schema, slow-subscriber isolation, and a browser-safe bridge are
tracked in the UI-stream protocol TODO.

## Updates

```bash
figaro update                   # check for newer release
figaro update --apply           # go install the latest tag
```

## Releasing

```bash
grep -n '^replace' go.mod && echo "strip before tagging" || echo ok
git tag vX.Y.Z && git push origin vX.Y.Z
# smoke test:
go install github.com/jack-work/figaro/cmd/figaro@vX.Y.Z
# then bump flake.nix vendorHash if needed
```

## Nix flake

```bash
nix build                       # produces result/bin/figaro + fig symlink
nix develop                     # dev shell with Go, tools, isolated hush
```

## License

[NON-AI MPL-2.0](./LICENSE). Copylot, source available for human use. Not for AI/ML training (LICENSE section 3.6).
