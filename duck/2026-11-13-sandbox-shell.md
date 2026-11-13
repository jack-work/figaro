# Sandbox shell, phase 1: leased sandboxes on spain

Status: second draft. The first (67d80435) had El Inspector XVI's approval
of its shape with conditions S1 to S8; this one folds those in and Gluck's
direction of 11-13 (via 48e285da), which moved three things: the exec
channel and the lease go through a node agent that can later sit on
another machine, the egress proxy knows a sandbox by peer credentials over
a unix socket instead of by network interface, and sandboxes get a
workload kind for placement. No code. Base impresario 66cc14e1 (v0.67.3);
slot pool facts from keel `lane/slot-pool` b5cfd301 (0.68.0) and slot-pool
X; spain facts from edge (failsafe-effects).

Terms: a **sandbox** is one nspawn container that runs tools for the
figaros bound to it. A **class** is a named kind of sandbox (`kfin-read`)
with its packages, egress grant and workload kind. A **lease** binds one
sandbox to a figaro lineage until release. A **node** is a machine that
hosts sandboxes; its **node agent** creates, resets and reports them. The
existing code calls a sandbox a "box" (`[sandbox.boxes]`, `figaro box
serve`) and its callback socket a "door"; the note uses those words only
when it names that code. Renames are the Engineer's naming audit.

## Gluck's direction, and where each lands

| # | direction | in this note |
|---|---|---|
| 1 | the daemon never runs in a sandbox; a figaro is bound to one; the sandbox may be on another node | the daemon reaches sandboxes only through a node agent's endpoint and an exec endpoint the agent names; phase 1 endpoints are unix sockets, the wire is a stream protocol that runs over TLS later |
| 2 | conversation figaros and job figaros not co-located | a class has a workload kind; the daemon (conversation) and job sandboxes are placed on nodes by kind; phase 1 is spain only, the field and the selection exist |
| 3 | shell always allowed; a tall fence | every class gets `bash` and `process`; the fence is nspawn, a userns uid range, no network interface but loopback |
| 4 | proxy knows the sandbox by SO_PEERCRED; box holds nothing; SPIFFE per node later | one egress proxy per node on a unix socket bound into each sandbox; maps the peer's host uid to the sandbox and its lease; attaches the credential and the sandbox's identity |
| 5 | outbound calls need an explicit grant per class | a class's egress list is its whole grant; empty means no outbound call at all |

## Where a bound figaro's tools run today

From the code (v0.67.3):

| piece | runs in |
|---|---|
| turn loop, provider calls, model credentials | the daemon (`internal/angelus/restore.go`) |
| `bash`, `process` | the sandbox: `sandbox.Open` dials the exec socket of the box `system.sandbox` names (`internal/sandbox/open.go`); `figaro box serve` in the container runs `box.exec` and `box.session.*` as the sandbox's uid in `/work` |
| `read`, `write`, `edit` | the sandbox, over the same socket (`box.fs.*`) |
| `say`, `search` (pool only) | the daemon |
| the `figaro` CLI inside the sandbox | only with a box door; then as `sandbox:<box>@local:box` |

So the daemon already stays outside, and nothing it holds crosses the exec
socket. The exec wire (`internal/sandbox/box/wire.go`) is newline JSON-RPC
over a stream; only the dial (`dial_linux.go`: openat2, a socket check,
then `unix`) is local. That is what (1) needs: a second node changes the
dial, not the protocol. spain runs this for box0..box2
on 0.67.1, unleased and never wiped.

The 0.68.0 pool's leased sandboxes have no exec and execute nothing, by
El Inspector's D1 and (a) to (e). The pool is not the lessor here: its
lessee is a broker principal and it has no route from Gluck's store (P3).
Reusing it would undo P3, D1, (a) and (b) at 1.5k to 2k lines (slot-pool
X). kfin-read is Gluck's own data and his own figaros, so the lessor
lives in the main daemon. The pool stays as shipped.

## The shape

```
 node: spain                                    upstream
 ┌───────────────────────────────────────────┐
 │ container@figaro: the daemon              │
 │   lessor ──agent.sock──▶ node agent        │
 │   tools  ──exec.sock───▶ sandbox kfin0     │
 │                          │ egress.sock     │
 │                          ▼                 │
 │                     egress proxy ──────────┼──▶ kfin (token + Sandbox-Id)
 └───────────────────────────────────────────┘
```

### The node agent (spain-flake, edge)

A small host service, its own uid, holding the class declarations
rendered by nix. figaro speaks one protocol to it and names no node
technology. Methods, newline JSON-RPC on `agent.sock`:

| method | answer |
|---|---|
| `classes` | each class: name, workload kind, sandbox count, free count |
| `lease {class, generation, figaro}` | `{sandbox, exec}`: the sandbox picked and the exec endpoint, after the agent records who holds it |
| `reset {sandbox, generation, nonce}` | after the reset ran: `{sandbox, generation, nonce, at}`; an error if any step failed |
| `held` | the agent's lease table, for restart recovery |

The reset receipt is the reply on the daemon's own connection, so nothing
else can forge it (S2, below). The agent runs the reset itself: stop the
container, start it fresh (the container is ephemeral, S1), wait for `figaro
box serve`, answer.

On spain, `agent.sock`, each sandbox's `exec.sock` and nothing else of
the agent's are bound into container@figaro, read-only mounts of
root-owned directories, as the exec dirs are today.

Later, on a second node, the same agent answers on a TLS endpoint
authenticated by node certificates (troupe intermediates or a SPIFFE
agent), and `exec` names a TLS endpoint on that node. The daemon config
names the node and its endpoint; nothing else changes.

### A class (spain-flake)

```nix
services.sandboxes.classes.kfin-read = {
  count    = 2;                         # kfin0, kfin1
  workload = "job";                     # or "conversation"
  packages = [ pkgs.kfin-cli pkgs.jq pkgs.curl ];
  egress = [{                           # the class's whole outbound grant
    name         = "kfin";
    upstream     = "https://kfin.kelliher.info";
    methods      = [ "GET" ];
    paths        = [ ... ];             # named by kfin's owner
    credential   = "kfin-read-box";     # client credentials, the proxy's
  }];
  tmpfs    = "512M";
};
```

Rendered per sandbox `kfinN`:

- `container@kfinN`: ephemeral (root on tmpfs, `--volatile=overlay` or
  NixOS `ephemeral = true`), own userns range of 65536, **no network
  interface but loopback** (`--private-network`, no veth), `/nix/store`
  read-only, no host D-Bus, no `/run/secrets`. `bash`, coreutils and the
  class's packages on the path. The shell is always there (3).
- `/run/egress/egress.sock` bound in: the node proxy's socket, the only
  way out. Every listed upstream is reached through it. A class with an
  empty egress list gets no socket (5).
- the exec socket dir bound out to the agent and to container@figaro.

### The egress proxy (spain-flake, edge)

One per node, its own uid, holding every class's credentials as systemd
credentials. It listens on one unix socket per sandbox, bound into that
sandbox alone, and on each connection reads SO_PEERCRED: the host uid
falls in exactly one sandbox's userns range, which names the sandbox, and
the agent's lease table names its class, generation and figaro (4). A uid
in no range, or a sandbox with no lease, gets a 403 and no upstream call.

For each request it:

- takes the egress entry by path prefix (`/kfin/...` → entry `kfin`);
  forwards only to that entry's fixed upstream, whatever Host, an
  absolute-form URI or CONNECT says; CONNECT refused;
- forwards only the entry's methods under its path allowlist;
- drops `Authorization`, `Cookie` and any `Sandbox-*` header from the
  sandbox, then adds its own token and `Sandbox-Id: kfin0-g7`
  (sandbox and generation) and `Sandbox-Figaro: <figaro id>`;
- logs one line per call: sandbox, generation, figaro, entry, method,
  path, status, bytes. Never a body. This is the per-sandbox identity
  report (4).

Inside the sandbox, `KFIN_URL=unix:///run/egress/egress.sock` for the kfin
CLI (it is Gluck's, so it takes a unix URL), and for any other CLI a
loopback forwarder (`socat TCP-LISTEN:8080,bind=127.0.0.1
UNIX-CONNECT:/run/egress/egress.sock`) started in the sandbox as its own
uid, which the peer check still maps to the same sandbox.

Why this and not a credential in the sandbox: a shell can print anything
it holds. Behind the proxy the most a sandbox can do is the calls its
class lists, with no network to send the answers anywhere else.

### kfin and Authelia (S8)

- Authelia client `kfin-read-box`: client credentials only, audience kfin
  alone, explicit audience mode (as herald-server).
- kweb's `bearerServices` for kfin only.
- kfin treats that caller (Remote-User = the client id, no groups) as
  read-only in kfin itself, and refuses it every GET with a side effect:
  sync, export, the Plaid refresh, and any other the kfin owner names when
  writing the change. `Sandbox-Id` and `Sandbox-Figaro` go in kfin's access
  log.

## What figaro owns

**1. Nodes in the config.**

```toml
[sandbox.nodes.spain]
agent    = "unix:///run/sandboxes/agent.sock"
workload = ["job", "conversation"]
```

The daemon asks each node's agent for its classes at start and on reload.
A class name is unique across nodes; two nodes offering one class is
refused in phase 1 (placement across nodes is phase 2). An endpoint scheme
other than `unix` is refused until the TLS transport exists.

**2. The lease is create and kill.** No new RPC method.

- `figaro new -S system.sandbox=kfin-read ...` (or an outfit) names a
  class. The daemon writes its ledger row `leasing` with a new generation
  (fsync, as slotpool's counter and floor do), calls the agent's `lease`,
  probes the exec endpoint (below), writes `leased` with the sandbox, and
  stamps `system.sandbox = kfin0` before the figaro is born. No free
  sandbox: refused, `no_free_sandbox`, with the counts.
- Placement (2): a class's workload kind picks the node. The daemon itself
  is the conversation side; a `job` class is never placed on a node whose
  `workload` list lacks `job`. With one node in phase 1 this is a config
  check and one test, so the second node is a config change.
- A figaro, its forks and its successors share the lease (one lineage, one
  sandbox: an accepted residual).
- Who may lease (S4): `use:sandbox:kfin-read`. In phase 1 that is the
  owner and figaros of his own lineage; a troupe peer, a sandbox principal,
  a pool guest, `herald-server` and a figaro narrowed by `system.scopes`
  without it are each refused.

**3. Staying bound (S3).**

- Naming a leased sandbox directly (`system.sandbox = kfin0`) at create,
  set, bind or import is refused to everyone, the owner included: the only
  way into one is through the lessor.
- A leased figaro, or any principal below the owner, cannot unset or
  change `system.sandbox` on a leased form: unbound, its tools would run
  in container@figaro. The owner's change ends that figaro's binding and,
  if it was the last, triggers release.

**4. Release and reset.** When no figaro or form names the sandbox (the
kill of the last one; a leased sandbox's purge grace is `0`):

1. Ledger row `releasing`, fsync. `box.session.killscope` for each figaro
   that held it (exists).
2. A fresh 256-bit nonce in the row, fsync; `reset {sandbox, generation,
   nonce}` to the agent.
3. The agent stops the container, starts it fresh, and replies with the
   receipt. The daemon takes it only if sandbox, generation and nonce
   match.
4. Probe over exec: as the sandbox uid, `find / -xdev -writable` lists
   only the expected empty dirs (`/work`, `/tmp`); no canary, no process
   but `figaro box serve`'s (S1). Pass: row `free`.
5. A failed step or no receipt in two minutes: `out_of_service`, an ERROR
   line. `figaro box reinstate kfin0` (owner) runs steps 2 to 4 again with
   a new nonce, and never marks `free` by itself (S7).
6. The probe runs again at lease, before the row goes `leased`.

Restart recovery reads the ledger and the agent's `held` before any
create: a leased row with no figaro naming it goes to release;
`releasing` restarts at step 2 with a new nonce; a sandbox the agent holds
that the ledger does not goes to release; a lost ledger resets every
sandbox before it is offered, generation from slotpool's floor rule.

**No callback socket in phase 1.** The sandbox has no way to call figaro.
The parent asks its child through the daemon and reads the answer there.
A class that wants the `figaro` CLI inside brings the box door and
`figaro_claim = "required"` back in its own change, for El Inspector.

Untouched: the pool daemon, other boxes and their doors, the provider
path, the guard except the S3 and S4 refusals.

## S2: who can reach the nonce and the reset

The ledger lives in the daemon's state dir, 0600, the daemon's uid. The
nonce goes to the agent only on the daemon's connection, and the receipt
comes back on it: there is no `reset/` file anyone could write, so knowing
the nonce forges nothing.

A bound figaro's bash runs in its sandbox and cannot see the ledger or
the agent socket. But on spain, figaros with no sandbox (`local`) run
their tools in container@figaro as the daemon's uid, and can read the
ledger and connect to `agent.sock`. Such a process is already the owner:
it can kill any figaro. What it gains here is the power to ask the agent
to reset a sandbox mid-lease (the probe at the next lease catches nothing
wrong; the live figaro loses its files). That is owner authority, not an
escape. El Inspector to rule whether that stands, or whether phase 1
needs every spain figaro bound to a sandbox first.

## Threat rows

| threat | answer | row |
|---|---|---|
| a lease reads the last lease's state (S1) | ephemeral container; reset is a fresh start; probe walks every writable path | as the sandbox uid, a canary into every dir `find / -xdev -writable` lists and `sleep 1d`; release; lease again: no canary anywhere, no sleep |
| a forged or stale receipt frees a dirty sandbox (S2) | receipt is the reply on the daemon's connection and must carry this release's nonce | agent fake answers with the old nonce: row stays `releasing`, then `out_of_service`; a process that writes to the agent socket cannot answer the daemon |
| a figaro skips the lease (S3) | direct name refused everywhere | create, set, bind, import of `kfin0`, each refused for the owner too; with the fence removed two figaros share a dirty sandbox (canary) |
| a figaro leaves its sandbox (S3) | unset or change of `system.sandbox` below owner refused | self unset refused; parent-narrowed unset refused; owner unset leads to release |
| the wrong principal leases (S4) | `use:sandbox:<class>` held by the owner and his lineage only | troupe peer, sandbox principal, pool guest, herald-server, narrowed figaro: each refused |
| the sandbox holds a credential | the proxy holds it | grep the sandbox's whole filesystem and `env` for the token's canary |
| the sandbox reaches more than its grant (5, S6) | no interface but loopback; the egress socket only | `ip addr` shows `lo` alone; host fe80 port 22, 127.0.0.1 of the host, spain's WAN v4/v6, its tailscale address, LAN, rou2, Authelia: all unreachable; a class with no egress has no socket |
| the proxy forwards more (S5) | fixed upstream, method and path allowlist, header strip | Host header, absolute-form URI, CONNECT; POST; a GET outside the paths; a sandbox `Authorization` and `Cookie` (dropped, the canary never reaches the upstream fake); a forged `Sandbox-Id` (replaced) |
| one sandbox speaks as another (4) | identity from SO_PEERCRED, not from anything the sandbox sends | kfin1 connecting to kfin0's socket path is impossible (not bound in); a uid outside every range gets 403; the upstream fake sees kfin1's id on kfin1's calls |
| kfin side effects (S5, S8) | read-only in kfin for the service caller; side-effect GETs refused | each named endpoint as `kfin-read-box`: refused; the token at every other kweb site: refused; a token with no audience: refused |
| the sandbox reaches figaro | no callback socket; the exec socket is dialed by the daemon | no figaro socket in the sandbox; sandbox root cannot connect to the daemon's |
| model credentials in the sandbox | provider calls run in the daemon | grep the sandbox for the rou2 client's canary |
| placement (2) | workload kind checked against the node | a `job` class on a node without `job` fails to load |
| exhaustion | count bounds concurrency; `sandboxes.slice` caps CPU and memory; tmpfs size | N+1 creates: last is `no_free_sandbox` |

Accepted residuals, written into the class's credo: kfin data goes to the
model provider (rou2 explabs) because the figaro reads it; prompt
injection from kfin data, with no write path; a lineage shares one
sandbox and one lease. Ops: `system.ttl` on the class outfit, so an idle
lease does not hold a sandbox forever.

## Acceptance for the first window (kfin-read)

1. The switch renders kfin0, kfin1, the agent and the proxy; all active;
   netmon clean.
2. `figaro new -S system.sandbox=kfin-read -- "what did I spend on
   groceries last month"`: the figaro runs `kfin` in kfin0 through the
   proxy and answers; `id`, `hostname`, `pwd`: the sandbox uid, kfin0,
   `/work`; kfin's log names `kfin0-g1` and the figaro.
3. A second create gets kfin1; a third gets `no_free_sandbox`.
4. Every threat row, on the VM test and again live.
5. Kill both: each resets, receipts match, probes pass, rows `free`.
6. The daemon's other boxes and figaros are unaffected; doctor residents 0.

## Plan and cost

| step | who | repo | size |
|---|---|---|---|
| 1. class module: ephemeral loopback-only containers, packages, egress socket bind; VM test | edge | spain-flake (`keel.nixosModules.sandboxes`) | 2 days |
| 2. node agent (lease, reset, held, classes) and egress proxy (peer cred, allowlist, header rules, log) | edge | spain-flake, or a small repo of its own (edge's call) | 3 to 4 days; two small programs, not a reverse proxy's config, because of the peer-credential map |
| 3. Authelia client, kweb `bearerServices`, kfin's read-only service caller | edge, kfin's owner | spain-flake, kfin | 1 day |
| 4. figaro: nodes, lessor, ledger (slotpool's counter and floor), agent client, probe, S3 and S4 fences, tests | one figaro lane | figaro | 800 to 1100 lines with tests; 4 to 5 days |
| 5. El Inspector reads 1 to 4 | | | a day of turns |
| 6. window: repin figaro, switch spain | edge; the King names it | spain-flake | one window |

About two weeks of wall time with the two lanes in parallel, 10 to 12
lane-days in all. Against the first draft: the agent and the peer-credential
proxy add two to three days on spain's side and about 300 lines in
figaro, and buy the node boundary (1) and per-sandbox identity (4) now.
rou2 is untouched.

## Later

- Phase 2, a second node: the agent and exec endpoints over TLS with node
  certificates; placement across nodes by workload kind; classes offered
  by more than one node.
- Workload identity: a SPIFFE-style agent per node. Upstreams authenticate
  the node (its SVID instead of a client-credentials secret), and the node
  vouches for the sandbox: the proxy's `Sandbox-Id` becomes a short-lived
  signed assertion bound to (sandbox, generation, figaro), minted at lease
  and dead at release. Per-lease rights (a parent narrowing what its child
  may read) plug in there, at the proxy.
- Guest shells in the pool: the pool's sandboxes take the same agent and
  reset, and its outfit may list the executing tools: El Inspector's (e),
  one change, his read.
- The callback socket for classes whose figaro needs the `figaro` CLI,
  with `figaro_claim`.
