# Figaro

*Largo al factotum della città.*

An LLM harness written in Go. One binary serves as a CLI, a supervisor daemon, and the agent runtime. Everything talks JSON-RPC over unix sockets — the wire is the API.

Figaro manages persistent conversations called *arias*. Each aria is an append-only message log in a provider-agnostic IR, with per-provider translation caches that preserve byte-stability for prompt caching. Arias bind to your shell by PID, survive daemon restarts, and can be addressed by name from any terminal.

Conversations **fork natively**: any past point is a branch you can take, sharing its parent's prefix instead of copying it — figaro is built on [figwal](https://github.com/jack-work/figwal), a segmented write-ahead log with copy-free forking. Branches form a *trunk forest* you navigate like a filesystem.

```
figaro -- explain this function
figaro -- now refactor it, per favore
figaro list
figaro send <id>:8 -- try it the other way   # fork at LT 8, continue on the branch
```

Also reachable as `fig` (installed alongside `figaro`).

The default verb is `figaro.qua` — the call that summons the barber.

## Shape of the thing

- Provider-agnostic message IR. Anthropic backend today; the interface is small.
- Native conversation forking on [figwal](https://github.com/jack-work/figwal): branch any past LT; `attend` is your `cd` through the fork forest (see [Forking](#forking)).
- Chalkboard: per-aria structured state that travels as patches on the message stream and surfaces to the model as system reminders.
- Tools: bash, read, write, edit. Parallel dispatch when the model emits multiple calls.
- Loadout system: TOML configs with inheritance, file/dir inlining, templated system prompts.
- Background derivations: per-aria views (usage, token, and cache stats) materialized each turn; surfaced in `list` and `status --more`.
- OAuth via [hush](https://github.com/jack-work/hush). Tokens encrypted at rest.

## Forking

Arias are *trunks* in a fork forest. Every turn has a logical time (LT, shown by `figaro show`), and any past LT is a branch point. Because figwal shares a branch's inherited prefix instead of duplicating it, forking is cheap and the original timeline is never disturbed.

```
figaro show                  # units labeled by LT
figaro send <id>:8 -- ...    # fork at LT 8; send to the new branch and move there
figaro fork                  # branch the tail imperatively (no prompt)
figaro attend <id>:8         # bind here; the next bare prompt forks at LT 8
figaro ls                    # the fork tree, rooted where you're attended
figaro promote <id>          # (planned) re-elect a branch as the canonical trunk
figaro kill <id>             # remove a trunk and everything forked below it
```

`attend` is your `cd`. Bound to a trunk, `figaro ls` shows that conversation's fork tree with `●` marking where you are; `figaro detach` (then `ls`) shows the whole forest; `figaro ls /` forces the whole forest even while attended. The **continuation line** — the path that keeps the conversation's id through each fork — is the canonical *trunk*; alternatives hang off it as branches. The `FORK` column in `list`/`ls` shows the LT each branch was taken at.

## Status

In active development. The core loop works and I use it daily. The goal is a general-purpose harness that can be scripted, composed, and extended to cover as much of my working surface as possible — not a product, but a tool that grows with the work.

## Setup

```bash
nix profile install github:jack-work/figaro      # or go install
figaro login anthropic
figaro -- buongiorno
```

If you installed via `go install` and want the short `fig` name as well:

```bash
ln -s figaro "$(go env GOPATH)/bin/fig"
```

The Nix package installs that symlink for you. The binary inspects
`argv[0]` so `fig --help` prints `Usage: fig ...` — same surface, shorter
name.

Config lives at `~/.config/figaro/`.

### Staying current

```bash
figaro update           # is there a newer release? print the exact upgrade command
figaro update --check   # skip the 24h cache and hit the module proxy now
figaro update --apply   # (go install channel only) shell out to `go install …@vX.Y.Z`
```

A one-line nudge also appears at the top of stderr when you run a
verb and a newer tag is available on the module proxy. Silence it in
`~/.config/figaro/config.toml`:

```toml
check_updates = false           # default: true
update_check_ttl_hours = 168    # default: 24
```

The check is TTY-only, cached, and never mutates the binary on its
own. Nix installs get an advisory pointing at `nix profile upgrade`;
dev-shell builds are skipped entirely.

## Releasing

Figaro is consumed via `go install` and via the Nix flake. Both pull from
git tags, so the release dance is just: keep `go.mod` clean, tag, push.

1. **Verify `go.mod` has no `replace` directives.** Local-path replaces
   make the module uninstallable from outside your machine — the Go
   toolchain refuses with *"go.mod file ... contains one or more replace
   directives"*. Use a `go.work` file for local development instead; Go
   ignores it when the module is fetched as a dependency.

   ```bash
   grep -n '^replace' go.mod && echo "strip these before tagging" || echo ok
   ```

2. **Tag and push.** Versions are immutable on `proxy.golang.org` once
   fetched, so a botched release means bumping — you cannot reuse a
   number.

   ```bash
   git tag vX.Y.Z
   git push origin vX.Y.Z
   ```

3. **Smoke-test from a clean machine** (or `GOPATH=$(mktemp -d)`):

   ```bash
   GOPROXY=https://proxy.golang.org go install \
     github.com/jack-work/figaro/cmd/figaro@vX.Y.Z
   ```

4. **Bump the Nix flake** if you want `nix profile install` to pick up
   the new version: update `version` and the `vendorHash` in
   `flake.nix`, commit, push.

---

*Tutti mi chiedono, tutti mi vogliono.* MIT.
