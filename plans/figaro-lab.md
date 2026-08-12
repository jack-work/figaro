# figaro-lab: running and testing figaro in containers on spain

**Status: proposal.** Nothing here is built. Written 2026-08-09.

The ask: run figaro inside containers, let it drive tmux and a pty the way it
does on this laptop, deploy that on spain as declarative NixOS services, and
let the deployed instances hunt bugs.

The good news is that figaro is already *most of the way* containerized and
nobody noticed: `mkFigaroShell`'s four knobs
(`FIGARO_RUNTIME_DIR`, `FIGARO_CONFIG_DIR`, `FIGARO_STATE_DIR`,
`FIGARO_CACHE_DIR`) plus `FIGARO_HUSH_*` are exactly the isolation contract a
container needs. A container is just a dev-shell preset with a kernel
namespace around it.

---

## 0. What is missing today (the honest inventory)

| Thing | State |
|---|---|
| CI of any kind | **none.** No `.github/workflows`, no `checks` in the flake. `go build && go vet && go test` runs only inside `scripts/release.sh` and in a human's shell. |
| flake outputs | `devShells`, `overlays`, `packages`. No `checks`, no `nixosModules`. |
| headless auth story | works but undocumented: `ANTHROPIC_API_KEY`, or `FIGARO_HUSH_DIR` + `FIGARO_HUSH_PASSPHRASE` (honored **only together**, so it can never touch the real keystore). |
| pty test suites | exist and are strong (`internal/cli/tmuxsmoke_*_test.go`, `scripts/paint-fuzz.sh`, `queue-fuzz.sh`, tapes) but are **all manually driven**. `ui-testing.md` says so in its own second paragraph. |

So the container work is not really "can figaro run in a container": it can.
It is "there is no unattended runner anywhere, and spain is the obvious place
to put one."

---

## 1. Three tiers, three different container shapes

Do not build one box that does everything. The tiers differ by an order of
magnitude in cost and in trust.

### T0: hermetic checks. No container needed; the nix sandbox *is* the container.

`go build ./... && go vet ./... && go test ./...`, plus the VT-model paint
tests, the tape replays (`go test ./internal/cli -run TestTape -tape …`), and
the store/schema tests. Deterministic, zero tokens, no secrets, no network.

This belongs in `checks.<system>` in the flake, not in an nspawn container:

```nix
checks = forAllSystems ({ pkgs }: {
  unit = self.packages.${pkgs.system}.figaro.overrideAttrs (o: {
    doCheck = true;
    checkPhase = "go vet ./... && go test -short ./...";
  });
  tapes = pkgs.runCommand "figaro-tapes" { ... } "…replay every testdata/tapes/*…";
});
```

Then spain runs `nix flake check` on a timer against the `main` branch. That
alone closes the largest gap in the project and costs nothing per run.

### T1: pty smoke and fuzz. This is what wants a real container.

`FIGARO_TMUX_SMOKE=1 go test ./internal/cli -run TestSmoke`, `paint-fuzz.sh`,
`queue-fuzz.sh`, `paintpane.sh` sweeps. Requirements the nix sandbox cannot
give you:

- a real `/dev/pts` and a real init (tmux wants both),
- persistent state across runs (`/var/tmp` scratch stores, tmux sockets),
- network egress to the provider,
- a long-lived angelus holding a store flock.

→ **systemd-nspawn**, per `~/notes/kelliher-web/nspawn.md`. `containers.<name>`
in `spain-flake`, `privateNetwork = true`, own systemd, own journal.

### T2: agentic bug hunters. Same container shape as T1, different payload.

figaro arias, credo and skills loaded, running the `ui-testing.md` procedure
*themselves*: pick a subsystem, write an oracle, prove it can fail against a
known-bad arm, drive tmux, capture, file the finding. This is the
"deploy them and have them find bugs" part, and it is the only tier that
spends real money per tick. It needs a governor (§5).

---

## 2. The container: what has to be in the closure and what has to be true

### Closure

```nix
environment.systemPackages = with pkgs; [
  figaro tmux git bash coreutils gnugrep gnused ncurses
  go gopls delta jq ncat        # go: smokeBinary builds FROM the worktree
];
```

`smokeBinary` in `tmuxsmoke_test.go` builds the binary from the tree under
test: identity by construction. So the container needs a Go toolchain, not
just the packaged figaro.

### Six things that will bite, in the order they will bite

1. **`/nix/store` is shared, `nix build` is not.** NixOS `containers.<name>`
   bind-mounts the host store read-only. Build *derivations on the host* and
   pass them in; do not try to run a nix daemon inside the container. For T0,
   the host builds `nix flake check` directly: no container at all.
2. **Go module cache.** A container with no network (or no `~/.cache/go-build`)
   cannot `go build`. Two options: (a) build the flake's vendor derivation on
   the host and bind-mount it with `GOFLAGS=-mod=vendor`, or (b) bind-mount a
   persistent `GOMODCACHE`/`GOCACHE` under `/var/lib/figaro-lab/go`. Prefer
   (a): it is hermetic and it is the same closure the flake ships.
3. **`/tmp` must not be the scratch dir.** `maintaining.md` is explicit:
   `/tmp` is tmpfs, i.e. RAM, and five 39 MB stamped builds there is 200 MB of
   memory. In a container with a `MemoryMax` this turns into an OOM that looks
   like a figaro bug. Bind-mount a disk-backed `/var/tmp`.
4. **Pane geometry is a test input.** The tmux-testing skill's eleven traps
   apply verbatim inside a container, and one of them (`new-session -e PATH=`
   silently ignored) is exactly the kind of thing that makes a containerized
   run compare a binary with itself. Always absolute paths into the pane.
5. **The angelus is a flock singleton per store.** One aria-store per
   container, or one per worker with distinct `FIGARO_STATE_DIR`. Two daemons
   on one store *contend*; they do not merge. Set `FIGARO_NO_SELF_SPAWN=1` in
   harnesses that must never accidentally start a daemon.
6. **Never seed a container with the real aria store.** `~/.local/state/figaro/arias`
   is ~130 MB of Jack's own conversations; a captured pane is a photograph of
   one. `.#snapshot` is a host-only preset. Lab containers get synthetic
   fixtures (`FIGARO_PERF_FIXTURE`, `FIGARO_VAL_FIXTURE`,
   `FIGARO_ORPHAN_CORPUS`) or committed tapes, reviewed before they land.

### Secrets

sops in `spain-flake` → systemd `LoadCredential` → env for the unit. Two
supported shapes:

```
ANTHROPIC_API_KEY=…                       # simplest; no keystore at all
FIGARO_HUSH_DIR=/var/lib/figaro-lab/hush  # embedded, per-container identity
FIGARO_HUSH_PASSPHRASE=…                  #   honored ONLY with HUSH_DIR set
```

Never bake a key into the nix store (world-readable) and never bind-mount the
host's real `providers/` or hush identity into a container.

---

## 3. The fleet: bossman as the runner, kstack as the front door

`bossman` already is the fleet supervisor this needs: heartbeat manager,
worker roster, `figaro` backend (`figaro send -e -r --`), NDJSON event log on
a unix socket, HTTP/SSE, a web UI, `/state`, `/healthz`. Don't write a second
one.

```
Cloudflare tunnel ──▶ Caddy (spain :8780) ──┬─▶ Authelia 2FA
                                            │
                                            └─▶ reverse_proxy 10.233.1.2:8787
                                                    │  (bossman HTTP in the container)
                                                    └─ /  live log UI
                                                       /events?level=  SSE
                                                       /state          roster JSON
```

`lab.kelliher.info` behind Authelia, per the kstack recipe. The one caveat
nspawn.md already flags: `sites.<name>` assumes `root` XOR `proxyTo`, so a
container target needs `extraConfig = "reverse_proxy 10.233.1.2:8787"` (or a
two-line `containerAddress` option added to `kelliher-web`).

Egress: the container needs the provider API and GitHub, nothing else. Start
with `networking.nat` over `ve-+` (as in nspawn.md); tighten to nftables
allow-listing the two hostnames when it matters.

---

## 4. Where the code lives

| Repo | Change |
|---|---|
| `figaro-qua` | add `checks.<system>` (T0). Add `nixosModules.default` exposing `services.figaro-lab`: the unit, the state dirs, the credential wiring, the knob env. This is figaro's own contract; it should not live in spain-flake. |
| `figaro-qua/scripts` | make `paint-fuzz.sh` / `queue-fuzz.sh` **exit non-zero on a failing oracle** and emit machine-readable findings (they already print; make them report). Parameterize `/var/tmp/rb` and the tmux socket by env. |
| `bossman` | probably nothing. Possibly a `figaro-worker` backend variant that pre-seeds the worktree + knobs. |
| `kelliher-web` | the `containerAddress` (or accept `extraConfig`) two-liner. |
| `spain-flake` | `inputs.figaro`, `figaro.nixosModules.default`, `containers.figaro-lab`, sops entry, tfvars regen, deploy. |

---

## 5. The governor: the part that is not optional

A fleet of agents with a provider key, a shell, and a heartbeat is a machine
for converting money into logs. Before T2 ships:

- **Budget ceiling per day**, enforced outside the agent (token/spend counter in
  the runner; halt the heartbeat, don't ask the agent to be thrifty).
- **Kill switch**: `systemctl stop figaro-lab` must be sufficient, and the
  container must come back clean (`ephemeral = false` for state you want,
  scratch under a dir the unit wipes on start).
- **Egress allow-list**: an agent that can reach the whole internet on a
  machine that also hosts Authelia and lldap is a different risk class than a
  laptop. `privateNetwork = true` + NAT + allow-list, and no bind mount of
  `/run/secrets`.
- **Resource caps** in the unit: `MemoryMax`, `CPUQuota`, `TasksMax`. Runaway
  tmux/pty fuzz is the expected failure mode, not the exception.
- **Findings are artifacts, not chat.** Every hunt writes to
  `/var/lib/figaro-lab/findings/<date>-<name>/`: the oracle, the **red arm**
  (proof the oracle can fail: `ui-testing.md` P2 requirement 1), the tmux
  captures, a tape if one was recorded, and a branch with a failing test.
  A finding with no red arm is discarded automatically.

---

## 6. Phasing

**Phase 1: flake `checks` (a day, no container, no spain).**
`checks.unit`, `checks.vet`, `checks.tapes`. Run `nix flake check` locally.
Deliverable: the project has an automated gate for the first time.

**Phase 2: the lab container on spain, T0 only (a day).**
`services.figaro-lab` in figaro's flake; `containers.figaro-lab` in
spain-flake; a systemd timer that fetches `main` and runs `nix flake check`,
journaling the result. No provider key yet. Proves the deploy loop.

**Phase 3: T1 in the container (the real work, 2–3 days).**
tmux + go in the closure, disk-backed `/var/tmp`, vendor dir bind-mounted,
provider key via sops. Make `paint-fuzz.sh` and `queue-fuzz.sh` CI citizens
with exit codes. First goal is boring: reproduce one *already-fixed* paint bug
in the container against its pre-fix commit. If the container cannot reproduce
a known bug, it cannot find an unknown one: that is the acceptance test for
the whole tier.

**Phase 4: bossman + the front door (a day).**
bossman in the container with the figaro backend, HTTP on the veth, Caddy site
behind Authelia at `lab.kelliher.info`. Watch a fleet of two workers do
something trivial (bump a doc) end to end.

**Phase 5: T2 hunters, gated (ongoing).**
Governor first, then one hunter on one subsystem with a hand-written brief.
Grade its findings by hand for a week before adding a second.

---

## 7. The one design opinion worth arguing about

Resist Docker/podman here. It buys an image registry and a daemon and costs the
real init, the clean `/dev/pts`, the shared `/nix/store`, and the "declared in
the flake, comes and goes with `nixos-rebuild switch`" property that makes
everything else on spain legible. nspawn is the right first stage, and
nspawn.md already argued this. If a hunter ever needs to be genuinely
untrusted: a fuzz that writes arbitrary files, an agent with a stolen prompt , 
that is when you reach for a VM (microvm.nix), not for Docker.

## 8. What this does NOT solve

- **"does it look right to a human?"** stays unanswerable by a container.
  Tapes are the bridge: a container can *record* one, a human plays it back.
- **Cross-platform paint bugs.** spain is x86_64-linux. The Darwin arms of the
  matrix have no runner and this plan does not give them one.
- **Provider drift.** The smoke suite is deliberately non-hermetic: it hits a
  real provider. A container makes that cheap to run repeatedly, which makes
  provider-side changes show up as flakes. Budget for triage, or accept that
  red is sometimes Anthropic's fault.
