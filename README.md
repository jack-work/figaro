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
