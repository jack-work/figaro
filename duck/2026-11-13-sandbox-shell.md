# Sandbox shell, phase 1: leased sandboxes on spain

Status: approved by El Inspector XVI (3a507b5d), U1 to U3 folded in
below ("Unbound figaros on spain"). The first draft (67d80435) had his
approval of its shape with conditions S1 to S8; the second folded those in and Gluck's
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
 node: spain
   container@figaro: the daemon
     lessor ── agent.sock ──▶ node agent
     tools  ── exec.sock  ──▶ node agent ──▶ sandbox kfin0
                                                │ egress.sock
                                                ▼
                                           egress proxy ──▶ kfin
                                           (token, Sandbox-Id, Sandbox-Figaro)
```

### The node agent (spain-flake, edge)

A small host service, its own uid, holding the class declarations
rendered by nix. figaro speaks one protocol to it and names no node
technology: "Node agent protocol, v1" below, the contract between figaro
and edge's agent.

The reset receipt is the reply on the daemon's own connection, so nothing
else can forge it (S2, below). The agent runs the reset itself: stop the
container, start it fresh (the container is ephemeral, S1), wait for `figaro
box serve`, answer.

The agent also serves each sandbox's exec endpoint (U2). It listens on
`exec.sock` per sandbox on the host side, checks SO_PEERCRED on every
connection, and splices an admitted one to the `figaro box serve` socket
inside the sandbox, whose directory only the agent's uid can enter. The
check cannot live in `box serve`: inside the sandbox's user namespace the
kernel reports every host uid it does not map, the daemon's included, as
the overflow uid, so the sandbox side cannot tell the daemon from anyone
else. On the host the agent sees the real uid.

Both `agent.sock` and every `exec.sock` admit exactly the daemon's host
uid (on spain the figaro container's mapped figaro uid, from
`fc.uidBase + fc.uid`, rendered by nix) and refuse every other uid, host
root included, beyond what the file modes already do. On spain those
sockets and nothing else of the agent's are bound into container@figaro,
read-only mounts of root-owned directories, as the exec dirs are today;
no sandbox has either bound in.

The agent logs (U3), one line each: every `lease` and `reset` with the
peer uid, sandbox, generation and the figaro id the daemon supplied; every
exec connection with the peer uid, sandbox and generation, and every
`box.open` with the figaro id it carries (the wire's `scope`). With the
proxy's per-call line, the chain from lease to exec to upstream call reads
after the fact by sandbox and generation. The figaro ids in it are what
the daemon said, which is as good as U1 makes the daemon's uid.

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

## Node agent protocol, v1

Status: proposed by the figaro lane, 11-13; becomes the contract when edge
writes "agreed" here. Neither side codes against it before that. A change
after that is a new version, agreed here first.

**Transport.** A unix stream socket (phase 1). JSON-RPC 2.0, one JSON
object per line in each direction, UTF-8 (figaro's `jkrpc`, as the exec
wire). The daemon is the only client; it may hold one connection or open
one per call, and the agent serves calls on a connection concurrently or
in order, its choice. On accept the agent reads SO_PEERCRED and closes any
connection whose uid is not the daemon's host uid, before reading a byte
(U2). No call carries a credential.

**Names.** A class name and a sandbox name match `[a-z][a-z0-9-]{0,31}`,
are unique on the node, and a sandbox name does not end in `-g<digits>`.
A generation is a positive integer. A figaro is 32 lowercase hex
characters (it goes into a header and every log line). A nonce is 64
lowercase hex characters. A reason is at most 200 characters. Times are
RFC 3339 UTC. Anything malformed answers `invalid_params`.

**Paths.** An exec URL names a host path that container@figaro binds at
the same path. spain renders `unix:///run/sandbox-agent/agent.sock` and
`unix:///run/sandbox-agent/exec/<sandbox>.sock`.

**Persistence.** The agent's table (each sandbox's state, generation,
figaro and last receipt, and each class's floor) survives the agent's
restarts and host reboots. A sandbox found `resetting` at agent start
stays `resetting` until the daemon calls `agent.reset`: the agent never
frees anything on its own (S7).

**Methods.**

`agent.hello {}` →
`{"protocol": 1, "node": "spain"}`. The daemon calls it on every new
connection and refuses any other `protocol`.

`agent.classes {}` →

```json
{"classes": [{
  "name": "shell",
  "workload": "job",
  "sandboxes": ["shell0", "shell1"],
  "writable": ["/work", "/tmp"],
  "egress": ["kfin"]
}]}
```

`workload` is `job` or `conversation`. `writable` is every path the
sandbox uid can write after a reset; each is empty then (the probe checks
both). `egress` names the class's egress entries, for the daemon's log and
`figaro sandbox classes`; it carries no upstream or credential.

`agent.sandboxes {}` →

```json
{"sandboxes": [{
  "name": "shell0", "class": "shell",
  "state": "leased", "generation": 7, "figaro": "<id>",
  "exec": "unix:///run/sandbox-agent/exec/shell0.sock"
}]}
```

`state` is `free`, `leased`, `resetting` or `cordoned`. `exec` is present
in every state, so a daemon that restarts between a receipt and its probe
can still probe. `figaro` is present when `leased`. `generation` is
present once the sandbox has had a lease, naming the last one.

`agent.lease {"class", "generation", "figaro"}` →
`{"sandbox", "generation", "exec"}`. The agent picks a `free` sandbox of
the class, records it `leased` to that generation and figaro, and answers.
`generation` is chosen by the daemon and rises per class; the agent keeps
the highest it has seen per class across its own restarts and refuses
one not above it, so it is the daemon's second floor. A repeat of a call
already answered (same class, generation, figaro) gets the same answer,
so a lost reply is safe to retry. `exec` is a URL; v1 has `unix://` and an
absolute path only.

`agent.reset {"sandbox", "generation", "nonce"}` → after the reset has
run: `{"sandbox", "generation", "nonce", "at"}`. Allowed from `leased`
at that generation, from `resetting` at that generation (a retry), from
`free` when the sandbox's last generation is the call's (the daemon lost
the receipt and asks again with a new nonce), and from `cordoned`
(reinstate: always a full reset). A reset call while that sandbox's reset
is running waits for it, then is judged on the state it left. The agent sets
`resetting`, stops the container, starts it fresh, waits until the
sandbox's `figaro box serve` accepts on the exec endpoint, sets `free`,
and answers. A repeat with the same nonce after success answers the same
receipt without resetting again; a new nonce resets again. A step that
fails sets `cordoned` and answers `reset_failed`. The daemon waits up to
120 s.

`agent.cordon {"sandbox", "generation", "reason"}` → `{}`. Sets
`cordoned` from `free`, `leased` or `resetting`: the daemon's probe
failed, or the daemon gave up waiting. Only `agent.reset` leaves
`cordoned`. `reason` is at most 200 characters, for the log.

**Errors.** JSON-RPC error code `-32060`, `data.reason` one of:

| reason | when | `data` also has |
|---|---|---|
| `unknown_class` | lease of a class the node does not have | |
| `no_free_sandbox` | lease with none `free` | `class`, `sandboxes`, `leased`, `resetting`, `cordoned` |
| `generation_used` | lease at or below the class's floor | `floor` |
| `unknown_sandbox` | reset or cordon of a name the node does not have | |
| `wrong_state` | reset or cordon not allowed from the sandbox's state | `state`, `generation` |
| `reset_failed` | a reset step failed; the sandbox is now `cordoned` | `step` |
| `invalid_params` | a name, figaro, nonce, generation or reason out of shape | `field` |

Any other error is the agent's fault: the daemon logs it and treats the
call as failed.

**What the daemon does with it.**

- Lease: ledger row `leasing` at the next generation (fsync), then
  `agent.lease`, then the probe over `exec`, then row `leased` (fsync).
  A failed probe: `agent.cordon`, the lease fails, the row closes.
- Release: row `releasing` with a fresh nonce (fsync), `agent.reset`, the
  receipt checked field by field, then the probe; pass: row `released`.
  Probe failure or no receipt in 120 s: `agent.cordon`, row
  `cordoned`, an ERROR line. `figaro sandbox reinstate <name>` (owner)
  runs the release again with a new nonce.
- Start: `agent.hello`, `agent.classes`, `agent.sandboxes`, then
  reconcile against the ledger before any create (a sandbox the agent
  holds for a generation the ledger does not hold as `leased` gets a
  release; a ledger row the agent does not hold closes).

**Probe** (the daemon's, over exec, as the sandbox uid): one `sh -c` that
prints the output of `find <each writable> / -xdev -writable` and the
pids, uids and command names under `/proc`. Pass when every writable path
found is in `writable`, each `writable` dir is empty, and no process of the
sandbox uid runs besides `figaro box serve` and the probe's own. So every
class has `sh`, `find` and `figaro` on its path.

**Sandbox side, for edge.** The exec socket the agent serves speaks the
existing exec wire (`internal/sandbox/box/wire.go`) unchanged. The relay
admits a connection, after the peer check, only while the sandbox is
`free` or `leased`; in `resetting` or `cordoned` it closes it and logs
that. It reads only the first line of each connection: it must be
`box.open` or `box.session.killscope` and at most 1 MiB, or the agent
logs `refused` and closes without forwarding a byte. It logs the method
and, for `box.open`, the `scope` (the figaro id), never `env` or
`workspace` (U3). Today's daemon sends `box.open` first on every
connection, the scope kill's included.

**Egress identity.** `Sandbox-Figaro` names the figaro that leased the
sandbox, not the fork in its lineage that made a given call: the proxy
cannot see past the sandbox. Each container runs `figaro box serve` as
the sandbox uid at boot, listening on the path the agent splices to.

## What figaro owns

**1. Nodes in the config.**

```toml
[sandbox.nodes.spain]
agent    = "unix:///run/sandbox-agent/agent.sock"
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

Restart recovery reads the ledger and the agent's `agent.sandboxes` before any
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

## S2 and U1 to U3: unbound figaros on spain

The ledger lives in the daemon's state dir, 0600, the daemon's uid. The
nonce goes to the agent only on the daemon's connection, and the receipt
comes back on it: there is no reset file anyone could write, so knowing
the nonce forges nothing.

A bound figaro's bash runs in its sandbox and reaches neither the ledger
nor the agent. A figaro with no sandbox (`local`) on spain runs its tools
in container@figaro as the daemon's uid. Such a process passes U2: it can
ask the agent to reset a sandbox mid-lease, and it can dial a sandbox's
exec endpoint and run `kfin` there, which the proxy attributes to that
sandbox's lease. El Inspector accepted this as owner authority on one
condition: on spain an unbound figaro is only ever the owner's (U1).

What makes that true in the code and spain's policy today (impresario
66cc14e1, spain-flake master ced503b `figaro-door.nix`):

- Unbound means `system.sandbox` is empty or `local`, and binding a figaro
  there needs `use` on `sandbox:local` (`authz.Sandbox("")` is
  `sandbox:local`). `grantSandbox` asks it at create, fork, bind, import
  and set. A caller without it is refused, or, if it is a sandbox
  principal, pinned into its own sandbox (`homeSandbox`).
- `use on sandbox:local` is local-only: no troupe policy can lend it
  (`localOnlyRights`).
- spain's policy grants it only through the `owner` role's `*:*`, bound to
  the container's own uid and `user:admin@auth.kelliher.info` (Gluck's 2FA
  session). The `box` role (box0, chat, receipt) holds `create:*` and
  `use:sandbox:self`: its creations are pinned into its own box.
  `lent-to-troupe` holds `create:*` and verbs on `unboxed`, but not `use`:
  its creations and forks of an unbound figaro are refused.
  `herald-server` is bound to no role on master.
- A figaro narrowed by `system.scopes` is judged by its own set too
  (`AllowFigaro`), so without `use:sandbox:local` it cannot make an
  unbound figaro.

Two things this does not cover, for El Inspector:

- `lent-to-troupe` holds `send`, `set` and `fork` on `unboxed`. A troupe
  peer cannot make an unbound figaro, but it can prompt the owner's
  unbound figaros, and their tools then run unboxed. That stays owner
  authority while troupe `kelliher` holds only Gluck's own machines (today
  spain and his desktop); a row checks the member list at the window, and
  any new member is a new read.
- decide-narrow (herald's `decide:outfit:herald-decide`) is not on
  master. Before it lands: the figaro a decide call runs is bound to a
  sandbox or carries no executing tool. A row, for the decide-narrow lane.

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
| a non-owner makes an unbound figaro (U1) | `use:sandbox:local` is owner-only and local-only | a troupe peer, herald-server, a box principal (box0, chat, receipt), a pool principal and a scopes-narrowed figaro each create, fork, bind, import or set a figaro to no sandbox: refused, or pinned into its own box (box principals); spain's rendered policy grants `use:sandbox:local` to the `owner` role alone |
| the troupe drives an unbound figaro (U1) | owner authority while the troupe is Gluck's machines | at the window, `figaro troupe policy show kelliher` lists exactly spain and Gluck's desktop. Standing rule (El Inspector XVI): adding a troupe member, or any grant that reaches `unboxed`, reopens this read before it lands |
| decide runs tools unboxed (U1) | decide-narrow's figaro is bound or has no executing tool | for the decide-narrow lane, before it lands |
| another uid reaches the agent or a sandbox (U2) | SO_PEERCRED on the host side admits the daemon's host uid alone | a second host uid and host root connecting to `agent.sock` and to an `exec.sock`: refused and logged; from inside a sandbox neither socket exists; a sandbox uid connecting to its own `box serve` socket: not reachable (dir is the agent's) |
| the chain cannot be read back (U3) | agent logs lease, reset, exec and `box.open`; proxy logs each call | after the acceptance run, from the logs alone: for `kfin0-g1`, the lease (peer uid, figaro), each exec connection and open, each kfin call, and the reset |
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
| 2. node agent (the v1 protocol, the peer-checked exec relay, logs) and egress proxy (peer cred, allowlist, header rules, log) | edge | spain-flake, or a small repo of its own (edge's call) | 4 to 5 days; two small programs, not a reverse proxy's config, because of the peer-credential checks |
| 3. Authelia client, kweb `bearerServices`, kfin's read-only service caller | edge, kfin's owner | spain-flake, kfin | 1 day |
| 4. figaro: nodes, lessor, ledger (slotpool's counter and floor), agent client, probe, S3 and S4 fences, tests | one figaro lane | figaro | 800 to 1100 lines with tests; 4 to 5 days |
| 5. El Inspector reads 1 to 4 | | | a day of turns |
| 6. window: repin figaro, switch spain | edge; the King names it | spain-flake | one window |

About two weeks of wall time with the two lanes in parallel, 11 to 13
lane-days in all (the exec relay adds a day on spain's side; figaro is
unchanged by U1 to U3, which are proofs of today's code and spain
config). Against the first draft: the agent and the peer-credential
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
