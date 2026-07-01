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
nix develop .#share-hush     # isolate runtime/config/state; share hush — real OAuth + keys
nix develop .#share-config   # isolate runtime/state + run an embedded dev hush; share config
nix develop .#clean          # fully hermetic; triggers first-run flow
nix develop .#swap           # swap nix-profile to the worktree binary, restore on exit
```

The two "share-*" presets cross opposite axes — pick by what you're
actually exercising:

- **`share-hush`** keeps the real hush agent (and so real provider OAuth
  registrations + AGE-encrypted keys) reachable, but isolates config —
  you'll hit the first-run loadout picker. Use this when the *credential
  resolution / refresh path* is what you want to test against a live
  provider.
- **`share-config`** keeps the real loadouts/`providers/*.toml` reachable
  but runs an **embedded dev hush** rooted at `$FIGARO_DEV_ROOT/hush` with
  its own AGE identity, re-authenticated per shell (`fig login <provider>`
  or `ANTHROPIC_API_KEY=…`). AGE-ENC values in the shared config can't be
  decrypted by the fresh identity, so plan to re-auth. Use this when
  you're iterating on loadout/agent logic and don't care about exercising
  the real refresh wire.
- **`clean`** isolates everything including hush — the truth-test for the
  first-run flow and any auth migration.

All three persist their dev root across shell entries (`$FIGARO_DEV_ROOT`
is stable, not a fresh tmpdir). `rm -rf $FIGARO_STATE_DIR` for a clean
slate.

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
3. Exercise it in a dev shell (or a temp-dir `go build`), with
   `FIGARO_WIRE_DIR` when the wire matters. Pick the preset by what's
   under test: `.#share-hush` for credential/refresh work, `.#share-config`
   for loadout/agent iteration, `.#clean` for first-run / migration paths.
4. Update the docs that the change touched — this skill and its sections are
   the canonical record. A skill that lies is worse than no skill.

## Self-invocation

Figaro can call itself as a subagent — useful for isolated sub-tasks whose
context shouldn't pollute the current aria, or for pipe-friendly answers.
Two flags on `figaro send` do the heavy lifting:

- **`-e` ephemeral** — the aria is not persisted; nothing to clean up, no
  row in `figaro list` afterward.
- **`-r` raw** — plain text on stdout, stripped of ANSI/live-render chrome.
  Streamed, not buffered; safe to pipe.

The workhorse pattern is `figaro send -er -- <prompt>` — one-shot,
isolated, script-clean. Fan several out in parallel (background jobs or
`xargs -P`) when the sub-questions are independent.

For persistent sub-arias, use `figaro new -- <prompt>` (mints an id you can
keep talking to via `figaro send --id <id> -r -- <prompt>`), and
`figaro kill <id>` when done so they don't accumulate in `list`.

See the **figscript** skill for the full scripting recipe (parallel
fan-out, error handling, JSON extraction).

## Monitoring aria state (job polling)

Every aria carries a **`state`** field published by the daemon — the way
to tell whether a subagent is still working or has parked. Three values:

| state | meaning |
|---|---|
| `dormant` | not loaded in memory; nothing running |
| `idle` | loaded, inbox empty (no turn in flight) |
| `active` | inbox non-empty — currently working a turn |

Source: `state := "idle"; if !a.inbox.IsIdle() { state = "active" }` in
`internal/figaro/agent.go` (Agent.Info); `dormant` is stamped on by the
angelus when it merges disk-backed arias into the list response
(`internal/angelus/protocol.go`).

**One-shot poll (scriptable):**

```sh
figaro list -j | jq -r '.[] | select(.kind=="conversation") | "\(.state)\t\(.id)\t\(.mantra)"'
```

**Just one aria:**

```sh
figaro status <id> -j | jq -r .state
# or: figaro list -j | jq -r '.[] | select(.id=="<id>") | .state'
```

`figaro status <id>` (non-JSON) also prints it near the top.

**"Is anything working right now?" — exit-code style:**

```sh
figaro list -j | jq -e 'any(.state == "active")' >/dev/null && echo busy || echo quiet
```

**Live tail of one aria's frames** (pushed, not polled):

```sh
figaro listen <id>
```

Same live-render stream `send` uses mid-turn — tool calls and text as
they happen. Ctrl-D detaches without killing the turn.

**Caveats:**

- `active` is edge-triggered off inbox depth. A turn parked waiting on
  the provider still shows `active`; the flip to `idle` happens when the
  drain loop finishes the event.
- `last_active` (ms epoch, in `list -j` / `status -j`) is your recency
  signal for dormant/idle arias — pair it with `state` if you want
  "working *and* recently touched".
- There is no push notification of state transitions on the CLI surface.
  For reactive monitoring, poll `list -j` on an interval, or `listen` for
  the frame-level truth.

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
