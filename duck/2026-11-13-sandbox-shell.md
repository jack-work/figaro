# Sandbox shell, phase 1: leased nspawn slots on spain

Status: draft for El Inspector, then the Engineer and the King. No code.
Lane `design/sandbox-shell`, base impresario 66cc14e1 (v0.67.3). Facts
about the slot pool are from keel `lane/slot-pool` b5cfd301 (0.68.0) and
slot-pool X; spain facts from edge (failsafe-effects, 11-13).

Gluck's ask, as re-scoped on 11-13: a small pool of systemd-nspawn
containers on spain, leasable, each with its permissions (egress, CLIs,
credentials, shell) set in spain-flake, outside the figaro binary.
figaro's part as small as possible: lease a container and run a figaro in
it. Phase 1 is N slots, the lease, a figaro in a slot, and `kfin-read` as
the first class. Workload identity is a later phase (last section).

## Where a boxed figaro's tools run today

This decides the rest, so from the code (v0.67.3):

| piece | runs in |
|---|---|
| turn loop, provider calls, model credentials | the daemon (`figaro.NewAgent` in `internal/angelus/restore.go`) |
| `bash`, `process` | the box: `sandbox.Open` sees `system.sandbox` name a `[sandbox.boxes]` entry and dials its `exec` socket (`internal/sandbox/open.go`); `figaro box serve` in the container runs `box.exec` and `box.session.*` as the box's uid in `/work` |
| `read`, `write`, `edit` | the box, over the same socket (`box.fs.*`) |
| `say`, `search` (pool only) | the daemon (`toolsIn` swaps the registry for a leased box) |
| the `figaro` CLI inside the box | only through a box door, if the box has one; then it is `sandbox:<box>@local:box` |

So a boxed figaro already has a shell inside its container, and nothing the
daemon holds crosses into it: the exec socket carries commands in and bytes
out. spain runs this today for box0..box2 (plus chat and receipt), bound
into container@figaro, on figaro 0.67.1. What is missing is the lease: any
number of figaros may name box1 at once, and nothing wipes it between
uses.

In the 0.68.0 pool a leased box has no `exec`: `LeaseBox` builds
`{outfits, providers, purge_grace}` and the registry holds `say` and
`search` alone, by El Inspector's D1 and (a) to (e).

## Decision: a lessor in the main daemon, not the pool

`kfin-read` is Gluck's own work: his figaro, or one of his figaros' children,
reading his finance data and answering him. The figaro has to live where he
and his figaros can reach it. The pool daemon is built so they cannot (P3:
no route from Gluck's store, no grant crosses), and it is approved as a
daemon that executes nothing (P1, B2; `path = []`). Giving it shells
reopens that read for no gain to this class.

So phase 1 adds a lessor over `[sandbox.boxes]` in the main daemon (on
spain, the figaro container). The exec socket is local to that daemon, so
the lessor is spain's daemon: Gluck reaches it at fig.kelliher.info, and a
figaro on his desktop reaches it through the spain node, not its own
daemon. slot-pool X agrees this is the smaller path: reusing the pool for
a Gluck-side lessee needs a new remote principal and a route into a daemon
built to have none, which undoes P3, D1, (a) and (b). The pool stays as shipped. What the two
share is the reset protocol below, so the day guest figaros get a shell
(El Inspector's (e)) the pool takes the same slot shape and the same
spain module.

## What spain-flake owns (edge)

Everything that makes a class what it is. One declaration per class in
`sandboxes.nix`:

```nix
services.sandboxes.classes.kfin-read = {
  slots    = 2;                       # kfin0, kfin1
  packages = [ pkgs.kfin-cli pkgs.jq pkgs.bashInteractive pkgs.coreutils ];
  egress   = [ "proxy" ];             # nothing else; no DNS, no NAT
  proxy    = {                        # host side of each slot's veth
    upstream = "https://kfin.kelliher.info";
    methods  = [ "GET" ];
    serviceToken = "kfin-read-box";   # client credentials, held by the proxy
  };
  work = "tmpfs";                     # 512M; gone at every stop
};
```

It renders, per slot `kfinN`:

- `container@kfinN` as box0..2 are today: own userns range, private
  network, `/nix/store` read-only, no host D-Bus, no `/run/secrets`, the
  exec socket dir bound out to container@figaro. No door (below).
- the class's packages in the box's system path. That is the CLI list.
- an nftables allowlist: the box reaches the proxy's address and port on
  its veth, and nothing else; guard in mangle, as boxguard-net is now.
- `sandbox-proxy@kfinN`: a reverse proxy on the host end of the slot's
  veth, its own uid, holding the class's credential as a systemd
  credential. It forwards only the class's methods to the upstream and
  attaches the token. The box holds no secret. The proxy knows the slot by
  the interface the request came in on, which only that slot has.
- `sandbox-reset@kfinN`, a path unit on the slot's `reset/` dir (the
  protocol below).
- a VM test.

kfin's side: a new Authelia client `kfin-read-box` (client credentials),
named in kweb's `bearerServices` for kfin, and kfin accepting a service
caller (Remote-User = the client id, no groups) as read-only. Edge flags
that kfin has no service client today; that is kfin's one change.

Why a proxy and not a bound credential: the box can do anything a shell
can, so a bearer in the box is a bearer it can print. Behind the proxy, the
most a box can do with kfin is the GETs the proxy forwards, from inside a
box that reaches nothing else. This is Gluck's "handle it at the network
interface" without an issuer.

## What figaro owns

Three things.

**1. Classes in the config.** A `[sandbox.boxes]` entry may carry a class
and a reset dir:

```toml
[sandbox.boxes.kfin0]
exec     = "/run/sandboxes/kfin0/exec/exec.sock"
reset    = "/run/sandboxes/kfin0/reset"
class    = "kfin-read"
outfits  = ["kfin-read"]
providers = ["rou2-explabs"]
```

Rules at load: a class box has `exec` and `reset`, and no `door` or
`host_door` in phase 1; all boxes of a class carry the same lists; a class
name is not also a box name. spain-flake renders this table, as it renders
`[sandbox.boxes]` now.

**2. The lease is create and kill.** No new method.

- `figaro new -S system.sandbox=kfin-read ...` (or an outfit that says
  so) names the class. The daemon, holding a per-box lock, picks a `free`
  box of the class, writes the box's ledger row `leased` with a new
  generation (fsync), and stamps `system.sandbox = kfin0` on the form
  before the figaro is born. No free box: refused, `pool_empty`, with the
  counts.
- Naming a class box directly (`system.sandbox = kfin0`) at create, set,
  bind or import is refused to everyone: the only way into a class box is
  through the lessor. A fork of a leased figaro stays in its box and
  shares the lease; a successor inherits it, as `system.sandbox` already
  carries over.
- The lease ends when no figaro or form in the store names the box: the
  kill of the last one (and the purge grace, which a class box sets to
  `0`). Then the daemon runs the release.
- Who may lease: `use:sandbox:kfin-read`. The owner holds it; a figaro
  narrowed by `system.scopes` needs it. No new verb.

**3. Release and reset.** The protocol from the slot-pool note's first
draft (before 652ee83f), which El Inspector named in (e):

1. Ledger row `releasing`, fsync. `box.session.killscope` for every figaro
   that held the box (exists, `KillBoxScope`).
2. Write `reset/request` = `{generation, nonce}` (256-bit nonce, atomic
   rename).
3. `sandbox-reset@kfinN` (spain-flake) runs on the path: stop the
   container, drop `/work`, start it, write `reset/receipt` =
   `{generation, nonce, at}`. A failure writes no receipt.
4. The daemon takes the receipt only if it carries its nonce, then probes
   over the exec socket: `/work` lists empty, and no process runs but
   `figaro box serve`'s own. Pass: row `free`. Fail, or no receipt within
   two minutes: `out_of_service`, an ERROR line, and the box comes back
   only by `figaro box reinstate kfin0` (owner).
5. At lease the probe runs again before the row goes `leased`.

Restart recovery reads the ledger before any create: `leased` with no
figaro naming the box goes to release; `releasing` resumes at its step; a
lost ledger takes every class box through a reset before it is offered,
generation = one more than any receipt names.

Untouched: the pool daemon and its fences, box doors and host doors of
non-class boxes, the provider path, the guard's decision table except the
lessor's refusal of a direct class-box name.

### Why no door in phase 1

A door lets the box's processes call figaro as `sandbox:kfin0@local:box`.
`kfin-read` does not need it: the parent sends the child a question and
reads its answer through the daemon. Without a door there is no
`figaro_claim` to get right and nothing in the box that speaks to the
daemon, so the box's whole interface to figaro is the exec socket the
daemon dials. A class that wants its figaro to run `figaro` inside the box
brings the door and `figaro_claim = "required"` back, for El Inspector's
read, in its own change.

## Threat rows for El Inspector

| threat | answer | row |
|---|---|---|
| a lease reads the last lease's files or processes | reset stops the container and drops `/work` (tmpfs); receipt nonce + probe before `free` | lease kfin0, write a canary to `/work` and start `sleep 1d`; release; lease again: no canary, no sleep |
| a stale or forged receipt frees a dirty box | the receipt must carry this release's nonce; the box cannot write `reset/` (host path, not bound in) | write a receipt with the old nonce: box stays `releasing`; `out_of_service` after the timeout |
| a figaro skips the lease by naming `kfin0` | refused at create, set, bind, import for everyone, the owner included | each, alone, refused; with the fence removed the second figaro shares a dirty box (canary) |
| the box holds a credential | no secret in the box; the proxy holds it | grep the box's whole filesystem for the token's canary; `env` in the box |
| the box reaches more than kfin | nftables allowlist: the proxy only; proxy forwards GET to one upstream | from the box: LAN, WAN, spain's other ports, rou2, the figaro container, Authelia all time out; POST through the proxy refused |
| one slot uses another's proxy | proxy bound to its slot's veth address only | from kfin1, kfin0's proxy address times out |
| the box reaches figaro | no door; exec socket is dialed by the daemon, the box cannot connect out on it (C1/C2 rows of spain-boxes stand) | no door socket in the box; box root `connect` to the daemon's sockets fails |
| model credentials in the box | provider calls run in the daemon | grep the box for the rou2 client's canary |
| prompt injection from kfin data | the figaro can GET kfin and talk to its parent; it has no write path anywhere | accepted residual; named in the class's credo |
| exhaustion | slots bound concurrency; `sandboxes.slice` caps CPU and memory; tmpfs size | N+1 creates: last is `pool_empty` |

## Acceptance for the first window (kfin-read)

1. spain switch renders kfin0, kfin1, their proxies and reset units; all
   active; netmon clean.
2. `figaro new -S system.sandbox=kfin-read -- "what did I spend on groceries
   last month"`: the figaro runs `kfin` in kfin0, through the proxy, and
   answers. `id`, `hostname`, `pwd` in the box: box uid, kfin0, `/work`.
3. A second create gets kfin1; a third gets `pool_empty`.
4. Every threat row above, on the VM test and again live.
5. Kill both: each box resets, receipts match, probes pass, rows `free`.
6. The main daemon's other boxes and figaros are unaffected; doctor
   residents 0.

## Plan and cost

| step | who | repo | size |
|---|---|---|---|
| 1. class + proxy + reset module, VM test | edge | spain-flake (`keel.nixosModules.sandboxes`) | 2 to 3 days (edge's estimate) |
| 2. service client and read-only service caller | edge, kfin's owner | spain-flake (Authelia, kweb), kfin | half a day |
| 3. class lessor (reusing slotpool's ledger and generation code), reset protocol, probe, tests | one figaro lane | figaro | 500 to 800 lines with tests, about 200 of them the reset protocol (slot-pool X's estimate); 3 to 4 days |
| 4. El Inspector reads 1 to 3 | | | a day of turns |
| 5. window: repin figaro, switch spain | edge, King names it | spain-flake | one window |

About a week and a half of wall time with two lanes in parallel, seven to
nine lane-days in all. rou2 is untouched: the leased figaro is Gluck's,
on the main daemon's own client.

## Later

- Guest shells: the pool's slots take `exec` and `reset` with this
  protocol, and the pool's outfit may list the executing tools: El
  Inspector's (e), one change, his read.
- Doors for classes whose figaro needs the `figaro` CLI, with
  `figaro_claim`.
- Workload identity. The proxy here knows a slot by its interface, which
  is enough while every slot of a class gets the same rights. When rights
  differ per lease (a parent narrowing what its child may read), the lease
  needs a credential of its own: a short-lived token (minutes) the lessor
  asks a small minter on spain for at lease, bound to (box, generation),
  revoked at release, presented by the box to the proxy, which then
  attaches the service credential for exactly the rights the token names.
  The box still holds nothing long-lived. That is the SPIFFE-style step,
  and phase 1's proxy is where it plugs in.
