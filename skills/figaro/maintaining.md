# Maintaining figaro

For the owner and for contributors: how to change figaro without breaking
the running one, and how to hand work back so it can actually be validated.

Using figaro is a different job; that lives in [cli.md](cli.md) and
[agents.md](agents.md).

## The one rule: never test against the live daemon

Reinstalling into `~/.nix-profile` stomps the running angelus, the owner's
arias, and the hush identity. Work in a worktree, test through a dev shell.

The angelus is a strict singleton via an exclusive flock on
`<store>/arias/.daemon.lock`, taken before it opens the backend or binds the
socket, so a second daemon on the same store loses and exits without touching
anything. That protects against an accidental race. It does not make sharing a
store safe: the lock makes two daemons contend, it does not merge them.

## Worktrees

The repo is a treebear layout: a bare repo at `.bare`, worktrees as peers of
`main/`. One task, one worktree, cut from `main`.

```sh
git -C ~/dev/figaro-qua/.bare worktree add ~/dev/figaro-qua/<name> -b <branch> main
```

Never work in `main/` itself. It is the owner's checkout.

## Dev shells

**The dev shell is the default; a scratch `go build` is the exception.** Reach
for `go build` only for a wire-level probe where the binary's provenance cannot
matter — dumping request bodies, checking an exit code. Anything you intend to
BELIEVE about behaviour, and everything about rendering, goes through a shell.

The reason is not ceremony. A worktree `go build` differs from the flake build
in the Go toolchain, the dependency closure and the environment, and any of
those can decide whether a bug reproduces. One night of UI hunting was done
entirely on `go build -o /var/tmp/...` binaries stamped with `-ldflags`, which
looked equivalent and were not: Go 1.26.5 against the flake's 1.26.1. Stamping
a scratch build makes it LOOK like the real thing, which is what makes this
trap worth naming.

Four env knobs (`mkFigaroShell` in `flake.nix`) flip between share (`null`) and
isolate. Every preset is a choice about which ones to isolate.

| Knob | Holds |
|---|---|
| `FIGARO_RUNTIME_DIR` | socket, PID, bindings |
| `FIGARO_CONFIG_DIR` | config.toml, outfits, providers, credo, skills |
| `FIGARO_STATE_DIR` | aria store, OTel |
| `FIGARO_CACHE_DIR` | regenerable cache (update-check memo and friends) |
| `FIGARO_HUSH_APP` | provider credentials |

| Preset | Isolates | Reach for it when |
|---|---|---|
| `nix develop` | nothing | a quick look at the worktree binary |
| `.#share-hush` | config, runtime, state | testing credential resolution or refresh against a live provider. Real OAuth and AGE keys stay reachable, so you meet the first-run outfit picker. |
| `.#share-config` | runtime, state, hush | iterating on outfit or agent logic. Real outfits and `providers/*.toml`, but an embedded dev hush at `$FIGARO_DEV_ROOT/hush` with its own AGE identity. Re-auth each shell (`fig login <provider>` or `ANTHROPIC_API_KEY=...`); AGE-ENC values in the shared config cannot be decrypted by the fresh identity. |
| `.#clean` | everything | the truth test for the first-run flow and for auth migration |
| `.#snapshot` | runtime, state, hush-agent socket | **store migrations.** Seeds `$FIGARO_STATE_DIR/arias` with a `cp -a` COPY of your real arias and reads the real config. The only honest fixture for a migration is your actual data; the only safe one is a copy of it. |
| `.#swap` | nothing | swap the nix-profile binary for this build, restore on exit |

`.#snapshot` copies once per dev root and then leaves the copy alone, so a
half-migrated store survives for inspection. It is **disposable and not cheap**
(hundreds of MB):

```
figaro-snapshot-reseed     # discard the copy, take a fresh one
rm -rf $FIGARO_DEV_ROOT    # throw the whole thing away
```

Never run a migration against `~/.local/state/figaro/arias` directly. A
migration rewrites the store in place, and unlike a bad build it cannot be
undone by rebuilding.

Pre-set env vars win, so presets compose:
`FIGARO_HUSH_APP=figaro nix develop .#clean`. `$FIGARO_DEV_ROOT` is stable
across entries rather than a fresh tmpdir; `rm -rf $FIGARO_STATE_DIR` for a
clean slate. Inside a shell, `which figaro` must print `/nix/store/...`, never
`~/.nix-profile`.

That composition is how you TEST inside a shell without touching the owner's
data. The presets isolate by category, but any knob can be overridden per
invocation, and non-interactive work is what `--command` is for:

```sh
FIGARO_RUNTIME_DIR=/var/tmp/x/run FIGARO_STATE_DIR=/var/tmp/x/state \
  FIGARO_CONFIG_DIR=$HOME/.config/figaro \
  nix develop .#share-hush --command bash -c 'figaro send -e -- "…"'
```

Real credentials and real outfits, an isolated store and socket, the flake's
own binary — and no first-run picker, because the config knob was overridden.
A shell entry takes tens of seconds, so give it room in any timeout.

## Without a shell

For a wire-level experiment, build to scratch and point the runtime at scratch:

```sh
go build -o /var/tmp/x/figaro ./cmd/figaro
FIGARO_RUNTIME_DIR=/var/tmp/x/run FIGARO_STATE_DIR=/var/tmp/x/state \
  FIGARO_WIRE_DIR=/var/tmp/x/wire /var/tmp/x/figaro …
```

Inherit `FIGARO_CONFIG_DIR` and `FIGARO_HUSH_APP` when you want real
credentials. `FIGARO_WIRE_DIR` dumps raw HTTP request and response bodies.
`figaro rest` (an alias of `figaro stop`) retires the daemon after a rebuild;
the next command respawns it.

**Scratch builds belong on `/var/tmp`, not `/tmp`.** On this machine `/tmp` is
tmpfs, which is RAM: five 39 MB binaries there is 200 MB of memory. `/var/tmp`
is disk, though it survives reboot, so clean up after yourself.

## The loop

1. Change one slice.
2. `go build ./... && go vet ./... && go test ./...` stays green.
3. Exercise it in a dev shell, or with the scratch build above.
4. Update the docs the change touched. See [updating-docs.md](updating-docs.md).

Commits are itemized and self-contained. There is one real user, so a clean
design beats a compatibility shim.

For UI work, a pty is the only honest oracle: see
[reference/ui-testing.md](reference/ui-testing.md). To reproduce a rendering
bug from a real session rather than a guess, record and replay it:
[reference/tapes.md](reference/tapes.md).

## Handing work back: the owner validates in a dev shell

This is the most common flow there is, and agents get it wrong by default.

The owner tests from a `nix develop` shell in the flake, always. Not an
installed binary, not a `~/.nix-profile` swap, and not a binary an agent left
in a temp directory. A path under `/tmp` tells him nothing he can act on: he
cannot see which branch it came from, cannot rebuild it, cannot `git log` it,
and cannot iterate in it.

So when work is ready, the handoff is **three facts, in this order**:

```
<absolute worktree path>    ~/dev/figaro-qua/<dir>
<branch> @ <short sha>      feat/whatever @ abc1234
<which dev shell>           nix develop   (or .#share-hush / .#share-config / .#clean / .#swap)
```

Written out, that is the whole ritual:

```sh
cd ~/dev/figaro-qua/<worktree>   # the branch is already checked out here
nix develop                      # or a preset; `which figaro` must show /nix/store/...
figaro …                         # exercise it
```

Rules that follow from it:

- **Always name the worktree path.** If a branch has no worktree, create one
  before handing it over. "It is on branch X" is not a validation instruction;
  a path he can `cd` into is.
- **Say which preset, and why.** The presets differ by what they isolate.
  Naming one with no reason makes him guess.
- **Binaries an agent builds are for the agent's own verification.** Still
  build them, since an honest A/B needs two stamped binaries with distinct
  md5s, but never present one as the deliverable. If a built artifact genuinely
  helps, mention it after the path, branch and shell, as an extra.
- **Say what to try, gesture by gesture, and what the old behaviour was.**
  Otherwise he is validating against his memory of the bug.

## Contributors

Same rules, one addition: you are almost certainly not sharing the owner's
machine, so `nix develop .#clean` is your default. It isolates everything,
including credentials, which means you exercise the first-run flow on every
fresh shell. That is a feature: the first-run flow is the path most likely to
rot unobserved.

Read [updating-docs.md](updating-docs.md) before editing any documentation.
The layout has a rule, and it has a reason.
