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

## Core concepts

- **Arias**: persistent conversations, append-only IR log, fork-tree storage via [figwal](https://github.com/jack-work/figwal).
- **Forking**: branch any past LT; both sides share the prefix. `attend` is your `cd`.
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
figaro show <id> -n 5           last 5 messages
figaro set <key> <value>        patch chalkboard state
figaro status                   current aria info
figaro --help                   full command list
```

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
