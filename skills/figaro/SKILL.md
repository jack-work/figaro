---
name: figaro
description: Working on the figaro codebase itself — what it is, the safe dev-shell iteration loop, and an index into the architecture (IR/RPC/chalkboard/live-render/provider), aria reading, the mantra, and cache control. Read a section file when its topic is in play.
---

# Figaro

Figaro is a local coding-agent / orchestration daemon. One real user (its
author), maybe a few more — so don't agonize over backcompat; favor clean
design and itemized commits.

**Source:** `~/dev/figaro-qua/main` (treebear layout: the bare repo at
`.bare`, worktrees as peers of `main/`). One static Go binary, **three
roles** — `CLI`, the `Angelus` supervisor, and a per-aria `Agent`. All IPC
is JSON-RPC 2.0, NDJSON, over Unix sockets. A Nix flake builds and tests it.

This skill is **first-party**: it lives in the repo at `skills/figaro/` and
ships with the binary (`$out/share/figaro/skills`); the outfit loader merges
it under `dirName = "skills"`, with the user's `~/.config/figaro/skills`
overriding by name. Edit the source copy, not a config copy.

## The one rule: never test against the live daemon

Reinstalling figaro into `~/.nix-profile` stomps the running angelus, the
user's arias, and the hush identity. The angelus is a strict singleton via an
exclusive flock on `<store>/arias/.daemon.lock`, taken **before** it opens the
backend or binds the socket — so a second daemon against the same store loses
the lock and exits cleanly (a loser never opens the store or steals the live
socket). That protects against accidental races, **not** against you pointing
a test build at the real store: the lock makes them contend, it doesn't make
sharing safe. Always test a worktree build through a **dev shell** that puts
the freshly-built binary on `PATH` from the Nix store, isolated to taste:

```
nix develop                  # worktree binary; real config/runtime/state/hush
nix develop .#share-hush     # isolate runtime/config/state; share hush (real keys)
nix develop .#share-config   # isolate runtime/state; share config + hush  ← usual choice
nix develop .#clean          # fully hermetic; triggers first-run flow
nix develop .#swap           # swap nix-profile to the worktree binary, restore on exit
```

Inside a shell `figaro`/`fig`/`q` resolve to the same store binary; `which
figaro` should show `/nix/store/...`, not `~/.nix-profile`. Four knobs
(`mkFigaroShell`) flip between *share* (`null`) and *isolate*:
`FIGARO_RUNTIME_DIR` (socket/PID/bindings), `FIGARO_CONFIG_DIR` (config.toml,
loadouts, providers, credo, skills), `FIGARO_STATE_DIR` (aria store, OTel),
`FIGARO_HUSH_APP` (provider credentials). Pre-set env vars win, so presets
compose: `FIGARO_HUSH_APP=figaro nix develop .#clean`.

For a quick wire-level experiment without a shell: `go build -o /tmp/x/figaro
./cmd/figaro`, run it with `FIGARO_RUNTIME_DIR`/`FIGARO_STATE_DIR` pointed at
temp dirs (inherit `FIGARO_CONFIG_DIR`/`FIGARO_HUSH_APP` for real creds), and
set `FIGARO_WIRE_DIR=<dir>` to dump raw HTTP request/response bodies.
`figaro rest` redeploys the daemon after a rebuild (it respawns on the next
command). The shell here is zsh — globs abort on no-match.

## The iteration loop

1. Change one slice. Keep commits itemized and self-contained.
2. `go build ./... && go vet ./... && go test ./...` — keep it green.
3. Exercise it in a `.#share-config` shell (or a temp-dir `go build`), with
   `FIGARO_WIRE_DIR` when the wire matters.
4. Update the docs that the change touched — this skill and its sections are
   the canonical record. A skill that lies is worse than no skill.

## Sections (read on demand)

These live beside this file; read the one whose topic is in play.

- **architecture.md** — the three roles, the IR, the chalkboard, the
  JSON-RPC + live-render wire protocol, the live-render node model and
  painter invariants, the provider/translation layer, and storage.
- **arias.md** — how an aria is laid out on disk (the figwal trunk store) and
  the two ways to read one (the `figaro` CLI vs raw JSONL).
- **trunks.md** — the forking model: a trunk is a root-to-leaf path with a
  stable id, loadout/conversation cauterization, LT numbering, and the
  `attend`/`ls`(=`cd`)/`fork`/`kill` surface.
- **mantra.md** — maintaining your aria's mantra (the essence phrase shown
  in `figaro list`).
- **cache-control.md** — how automatic prompt caching works and how to
  override it.
