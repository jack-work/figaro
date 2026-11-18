# Figaro deployment modes

Status: proposal, revision 1, 2026-11-17. Nothing is built. Asked by the King
for Gluck, for Project Italy. Written by the deploy-modes lane (figaro
`4beb606b`, a fork of the Figaro Engineer) against figaro v0.74.0
(`9cb743df7`) and hush v0.15.1. The carrier for the root half was agreed
with El Mayor (figaro `35fb08eb`, holder of cheroot-config); see
[Hosts and carriers](#hosts-and-carriers).

## The question

Two independent choices decide how safe a figaro installation is.

- **Axis A, where the daemon and hush run**: as the user, or as their own
  system users.
- **Axis B, where tools execute**: inside the daemon's uid, in bwrap, in a
  broker running as the user, or in a sandbox on another machine.

This document names each option, gives its threat model, its cost and what
it breaks, shows which combinations make sense, and recommends one target
with a staged path to it.

## Names used here

| name | what it is |
|---|---|
| **home mode** | Axis A today. The angelus (the figaro daemon) and the hush agent run as the user's uid, spawned on demand by the CLI. No root needed. |
| **house mode** | Axis A target. The angelus runs as system user `figaro`, hush as system user `hush`, each in socket-activated systemd units. Needs root once, at install. |
| **bare tools** | Axis B option 1. Tools run as child processes of the angelus, under its uid: `system.sandbox = "local"` today. |
| **bwrap** | Axis B option 2. Tools run in a bubblewrap namespace on the daemon's host. Removed in figaro 0.59. |
| **the valet** | Axis B option 3, new. A per-user broker, `figaro-valet`, running as the user. The angelus reaches it as a box; it runs tools as the user, inside a fence built from timed grants it holds itself. |
| **BYOS** | Axis B option 4, "bring your own sandbox". A sandbox provider the angelus reaches as a box, which receives the figaro's id on every call and looks up that figaro's permissions itself. On spain this is sandbox-node. |
| **the cred socket** | hush's read socket, `GET /cred/<name>`, admitted by `SO_PEERCRED`. Exists today as `hush cred serve`. |
| **the drop box** | hush's write-only socket, new. A user signs in or pushes a secret through it and can never read one back. |
| **the owner** | The principal that holds every verb on a node. In home mode it is the daemon's own uid; in house mode it must be named, because the daemon's uid is no longer a person. |

Sources read, all at the revisions linked:

- figaro architecture, sections Authorization, Owner-held keys, Figaro
  scopes, Boxes, Removed: bwrap, Troupe keys:
  [skills/figaro/reference/architecture.md](https://keel.kelliher.info/_keel/jack-figaro/blob/9cb743df79dba4a93ad0c839a1a51cfa0014bacb/skills/figaro/reference/architecture.md)
- fan-out limits:
  [skills/figaro/reference/fanout.md](https://keel.kelliher.info/_keel/jack-figaro/blob/9cb743df79dba4a93ad0c839a1a51cfa0014bacb/skills/figaro/reference/fanout.md)
- figaro's NixOS module, the system-user angelus that exists today:
  [nix/module.nix](https://keel.kelliher.info/_keel/jack-figaro/blob/9cb743df79dba4a93ad0c839a1a51cfa0014bacb/nix/module.nix)
- hush's system minter and its NixOS module:
  [internal/credserve/server.go](https://keel.kelliher.info/_keel/jack-hush/blob/v0.15.1/internal/credserve/server.go),
  [nix/cred-module.nix](https://keel.kelliher.info/_keel/jack-hush/blob/v0.15.1/nix/cred-module.nix)
- the sandbox shell design, phase 1 and its phase 2 list:
  [duck/2026-11-13-sandbox-shell.md](https://keel.kelliher.info/_keel/jack-figaro/blob/c71cf036/duck/2026-11-13-sandbox-shell.md?view=preview)
- sandbox-node, the spain provider and egress proxy:
  [jack-sandbox-node v0.1.1](https://keel.kelliher.info/_keel/jack-sandbox-node/blob/v0.1.1/internal/provider),
  [spain-flake sandbox-node.nix](https://keel.kelliher.info/_keel/jack-spain-flake/blob/e778c4b24a50f744436605a4032c8736d6cfa349/sandbox-node.nix)
- El Portero's threat model, for the write-only spool pattern:
  [gluck-portero doc/THREAT-MODEL.md](https://keel.kelliher.info/_keel/jack-gluck-portero/blob/b1bf835f06929960db34f852944884e70b35ef65/doc/THREAT-MODEL.md)
- the charter's figaro aspect, which already has `my.figaro.mode = "system"`:
  [cheroot-config charter/modules/figaro.nix](https://keel.kelliher.info/_keel/jack-cheroot-config/blob/0ff095abefb6ecf07473dc9ed906983a7e14c19c/charter/modules/figaro.nix)
- captains-log: `2026/11/16/hush-pr2-review-and-gate.md`,
  `2026/11/16/issue-120-cred-socket.md`, `2026/11/14/kfin-read-grants-design.md`,
  `2026/11/13/edge-spain-sandbox-shell.md`,
  `2026/11/09/gluck-authz-sandbox-lineage-thoughts.md`,
  `2026/11/06/figaro-stores-permissions-sandboxes.md`, all under
  `/home/gluck/.captains-log/`
- Gluck's comments on the Engineer role form `@ee927149` (`comment_b6bceed0ee48`,
  `comment_a5cddc1e7c1c`, `comment_bc6310809c25`).

## Axis A: where the daemon and hush run

### A1, home mode (today)

The angelus runs as the user. The CLI spawns it on demand
(`figaro-angelus-*.scope` under the user manager), and it re-execs itself as
the embedded hush agent, whose socket is `$XDG_RUNTIME_DIR/hush`. The store is
`~/.local/state/figaro`.

**Threat model.** Everything that runs as the user is the daemon. A bare tool,
a compromised npm postinstall, a browser extension that gets a shell: each
can read every figaro's transcript off disk, call the CLI door
(`angelus.sock`), where `Peercred.Admit` stamps it `user:<uid>`, the owner,
and ask the hush agent for any credential. This is figaro issue
[jack-work/figaro#57, "an unboxed aria's tools reach the daemon as the owner"](https://github.com/jack-work/figaro/issues/57),
closed as parked. Scopes and fan-out limits hold unboxed figaros by honour
only (the fan-out proposal says so in its first section): a tool that drops
`FIGARO_ARIA` is judged as the owner.

**Cost.** None. No root, no install step, works on any Linux or macOS
account. That is why it must stay.

**What breaks.** Nothing; it is the baseline.

### A2, house mode (target)

The angelus runs as system user `figaro` (uid pinned, 2001 in
`nix/module.nix` today), its state in `/var/lib/figaro` (0700 figaro). hush
runs as system user `hush`, state in `/var/lib/hush` (0700 hush). Both are
socket-activated: systemd owns the sockets and their permissions, the
processes start on first connect.

**Threat model.** The user's processes are no longer the daemon. Something
running as the user can reach the CLI door and is judged as the user it is
(a principal the policy binds), but it cannot read the store, cannot forge
another figaro's log, and cannot ask hush for a credential: the cred socket
admits only the `figaro` uid. The daemon's uid holds the store, so whatever
runs as `figaro` is as strong as the daemon. That makes Axis B decisive:
**in house mode no tool may run as the daemon's uid** (see B1).

**Cost.** Root once, to create two users, the units and the sockets. The
owner must be named in config (`[authz] owner = "user:1000"` or a policy
binding), since the daemon's uid is not a person. Tools lose ambient access
to the user's files; they get it back only through the valet.

**What breaks.**

- On disk: the store moves from `~/.local/state/figaro` to `/var/lib/figaro`
  and changes owner. Config moves from `~/.config/figaro` to a read-only
  directory rendered by Nix (as `nix/module.nix` already does). Every
  figaro with `system.sandbox = "local"` or unset is refused at open until it
  names the valet or a box.
- In the protocol: the CLI no longer spawns a daemon (`FIGARO_NO_SELF_SPAWN=1`,
  already honoured). A policy that relied on "no policy means the daemon's
  uid holds everything" now grants nothing to anyone, by design; the module
  writes the owner binding.
- The embedded hush agent stops being used by figaro. Providers read through
  the cred socket (`[cred] socket`, shipped in 0.71 for figaro issue
  [jack-work/figaro#120, "provider credential from an external cred socket"](https://github.com/jack-work/figaro/issues/120)).

## Axis B: where tools execute

### B1, bare tools

Tools are children of the angelus, under its uid (`internal/tool/exec_local.go`).

**Threat model.** In home mode, the tool is the user: same reach as A1 above.
In house mode, the tool is the daemon: it can read and rewrite every
figaro's store, write its own `system.scopes`, and impersonate any figaro on
any box socket (the sandbox shell design's "trusting the scope", item 3).
That is strictly worse than home mode.

**Cost.** None.

**What breaks if removed.** In house mode, `local` must be refused outright
for figaros; only the angelus's own housekeeping runs there. In home mode it
stays as it is.

### B2, bwrap

`system.sandbox = "bwrap"` ran tools in a bubblewrap namespace on the host,
removed in figaro 0.59 (commit `c8c030c36`, "sandbox: delete the bwrap
provider"). Gluck on the Engineer form: "I'm not sure I even want the bwrap
dependency."

**Why it went.** In home mode bwrap ran as the user's uid and ro-bound `/`.
Every uid-checked socket under `$XDG_RUNTIME_DIR` stayed reachable, the CLI
door and the hush agent included, so a bwrap tool was still the owner. The
namespace hid files, not authority. Hiding the sockets meant a hand-kept
deny list that each new socket silently escaped.

**Why it might come back.** Not as a figaro sandbox provider, but as the
valet's fence. Once tools run as the user under the valet (B3), the
remaining job is to hide the user's own authority from them: the runtime
dir, `~/.ssh`, the ssh and gpg agents, any personal hush agent, and to show
only granted paths. Landlock (unprivileged, in-kernel, no binary) does the
path half, but as of Linux 6.12 it does not gate `connect()` to a pathname
unix socket, the exact hole that sank bwrap. A mount namespace closes it:
mount an empty tmpfs over `$XDG_RUNTIME_DIR` and bind in only the valet's
own door. That is what bwrap does.

**Why it might not.** The valet can make the same namespace itself with
`CLONE_NEWUSER | CLONE_NEWNS` from the Go standard library, plus Landlock for
paths, with no external binary. Both need unprivileged user namespaces,
which some distributions disable; the valet must then refuse to start
rather than run tools unfenced.

**Recommendation for B2.** It does not come back as `system.sandbox =
"bwrap"`. The valet's fence is written in Go (userns, mount namespace,
Landlock), with bwrap kept only as a fallback the valet may exec if the Go
fence proves too costly to maintain. Either way, the namespace is the
valet's business, not figaro's.

### B3, the valet (user broker)

A per-user service, `figaro-valet@<user>`, running as the user. It serves
the existing box protocol (`api/sandbox` over jkrpc: `box.open {scope}`,
`box.exec`, `box.session.*`, `box.fs.*`, the protocol `figaro box serve`
speaks today), so to the angelus it is just a box:

```toml
[sandbox.boxes.valet-gluck]
exec     = "/run/figaro-valet/gluck/exec.sock"
door     = "/run/figaro/boxes/valet-gluck/door.sock"
outfits  = ["*"]
providers = ["*"]
figaro_claim = "required"
```

On each connection it checks `SO_PEERCRED` is the `figaro` uid, reads the
figaro id from `box.open`, and runs that figaro's commands inside a fence
built from the grants it holds for that id.

**Grants.** A grant is held by the valet, written only by the user, and
names: the figaro id (or a lineage root), a set of paths with `ro` or `rw`,
network on or off, and an absolute expiry (capped, say 7 days, like the
kfin-read grants on spain). No grant: the figaro gets an empty scratch
workspace and nothing else. The user grants with
`figaro-valet grant <figaro> ~/dev/figaro rw --for 8h` from his own shell;
the valet's admin socket admits only the user's uid. A figaro asking for
more gets a refusal it can quote to Gluck, who grants or does not.

**Threat model.** The resource owner is the user, so the user's agent
enforces. The angelus can claim any figaro id, but it is trusted to do so
(it is the daemon; Gluck's comment `comment_b6bceed0ee48`: "on ingress, the
figaro daemon trusts the caller"), and a grant only ever widens a fence the
valet itself built. A tool inside the fence runs as the user but sees no
CLI door, no hush, no ssh agent: it calls back through the valet's box door,
where `BoxAdmit` makes it `sandbox:valet-gluck@local:box` and
`figaro_claim = "required"` pins it to its own figaro. This closes figaro
issue [jack-work/figaro#57](https://github.com/jack-work/figaro/issues/57)
for real, not by honour.

What it does not stop: a granted path is the user's, so a figaro granted
`~/dev/figaro rw` can plant a git hook the user later runs. Grants should
name working trees, not homes.

**Cost.** A new small program (Go, standard library plus jkrpc), one socket
unit pair per user, and a grant vocabulary. figaro lines: none in the
angelus beyond config; the box client already exists.

**What breaks.** Nothing on its own; it is opt-in per outfit
(`system.sandbox = "valet-gluck"`). In house mode it becomes the default
for owner-minted figaros.

### B4, BYOS (remote sandbox, spain sandbox-node)

The sandbox shell design, phase 1, live on spain since 2026-11-13: a
provider keyed on the figaro id serves one exec socket per class
(`/run/sandbox-provider/<class>.sock`), leases an ephemeral nspawn
container per figaro, resets it on `killscope`, and an egress proxy enforces
per-class (and with kfin-read, per-grant) egress. This is Gluck's "bring
your own sandbox": the sandbox receives the figaro's identity and looks up
its permissions itself (`comment_b6bceed0ee48`).

**Threat model.** Strongest isolation: a separate uid range, a separate
container, nothing of the user's or the daemon's inside. The provider trusts
the scope because only the daemon's uid connects (sandbox shell design, P4).
Across machines that trust has to travel: the daemon must prove which node
it is, and the sandbox must not take the claim from an IP address.

**Cost.** Today, local unix sockets only (`dial_linux.go`). A sandbox on
another node needs the phase 2 figaro line: a remote transport for the exec
wire, carrying `Figaro-Id`, authenticated by the troupe node certificate.
Per-figaro permissions inside a class are the provider's to read; it may
read `figaro.scopes` through a door limited to an existence verb (El
Inspector's P1).

**What breaks.** Nothing local. Remote BYOS adds a protocol (below, stage 7).

## The matrix

| | B1 bare | B2 bwrap | B3 valet | B4 BYOS |
|---|---|---|---|---|
| **A1 home** | today's desktop. Tools are the owner. Keep, labelled unsafe, as the no-root mode | dead: hides files, not sockets. No | possible but pointless as a boundary: the valet and the tool share a uid with the daemon. Useful only to test the valet before house mode | works today (spain shell from a home-mode CLI through the troupe). Safe for the tool, but the daemon itself still sits in the user's uid |
| **A2 house** | refused. A tool as the daemon's uid owns the store | only as the valet's fence (B3's implementation), never as figaro's provider | **target** for work on the user's files | **target** for everything else: untrusted code, guests, builds, data-scope grants |

Spain is already A2 plus B4 (`container@figaro` runs the angelus as the
container's system user; shells are sandbox-node), with one gap: unbound
figaros there still have bare tools as the daemon's uid (sandbox shell
design, U1, accepted as owner authority).

## Recommendation

**Target: house mode, with two tool homes, the valet and BYOS, and no bare
tools.** The valet for work that must touch the user's files, under timed
grants it holds; BYOS for the rest, locally (a sandbox-node on the host) or
on spain. Home mode stays a supported and tested mode, per El Mayor, for
hosts without root and for development, documented as "tools are you".

Why this one: each resource is enforced by its owner, which is Gluck's
standing preference (`comment_a5cddc1e7c1c`: permissions stored on the
object they protect). The daemon enforces figaro verbs; the valet, the
user's files; hush, secrets; spain, tokens. No component has to be trusted
for a resource it does not own.

## The grants rule: the resource owner enforces

| resource | enforcer | where the grant lives | exists today |
|---|---|---|---|
| figaro verbs (send, fork, read, kill, ...) | the angelus | policy file, plus `system.scopes` on the lineage | yes |
| fan-out | the angelus | `system.fanout.max_children` on the figaro form and its role forms (v0.74.0, `skills/figaro/reference/fanout.md`) | yes |
| the user's files | the valet | the valet's grant table, `~/.local/state/figaro-valet/grants` (0600, the user's) | no, stage 3 |
| secrets and plan credentials | hush | hush's config: which uid may get which instance; later which node | partly: one `allow_user` per minter |
| service tokens and data scopes (kfin, egress) | spain: sandbox-grant and the egress proxy | sandbox-grant's state on spain | yes for kfin-read, built, not yet deployed |

A figaro form **may mirror** grants read-only, so a figaro and its viewer
can see what it holds: `system.mirror.<enforcer>`, written only by that
enforcer's principal (the valet through its box door, hush through a
service principal), refused to everyone else, the owner included, and
**never read by any enforcer to decide anything**. A stale mirror shows
wrong information; it cannot grant.

## Hush custody

### Sockets

| socket | mode | who may connect | what it answers |
|---|---|---|---|
| `/run/hush/cred.sock`, the cred socket | 0660 `hush:figaro` | `SO_PEERCRED` uid = `figaro`, checked again in process (`allow_user`, as `hush cred serve` does today) | `GET /cred/<name>`: a short-lived bearer and its expiry. Never the refresh token. |
| `/run/hush/drop.sock`, the drop box | 0660 `hush:hush-drop` | members of `hush-drop` (the human users) | `POST /login/<name>` starts a sign-in (device code or PKCE; the user pastes `code#state` into his own terminal, never argv); `PUT /secret/<name>` pushes a sealed value. Replies carry status only, never a secret. |
| `/run/hush/admin.sock` | 0600 `hush` | root via `hush` | status, instance list, revoke. |

The drop box is El Portero's intake pattern applied to secrets: a public
side that can write and never read. `SO_PEERCRED` on the drop box records
who pushed, for the log; it does not authorise reading.

State: `/var/lib/hush`, 0700 owned by `hush`, one age-encrypted file per
instance (as today), the age identity delivered by `LoadCredential=` and
never at rest beside the state. The egress fence of `nix/cred-module.nix`
(nftables plus dnsmasq `nftset`, uid-matched) stays.

### Key hierarchy

One hierarchy, the troupe's, already in figaro and hush:

1. **Master** (the troupe root, Ed25519): held by Gluck offline. Today it
   lives in Gluck's desktop hush as the `troupe-root` command's secret, which
   is online; it moves to offline media and is used only to sign the spain
   intermediate, renewer entries and revocations.
2. **Intermediate** (spain): a one-year CA that signs only leaves under
   `spiffe://kelliher/`. Held on spain as a systemd credential
   (`[troupe] intermediate_key_credential`), never on disk. Spain is the only
   intermediate and every node's renewer.
3. **Node**: each node makes its key on tmpfs (`figaro troupe keygen`), gets
   a leaf from spain (`hush troupe-root leaf ... --for <node>`), and renews
   it from spain. The private key never travels.

Breakage: nodes that hold their own intermediate today (the desktop) give
it up and become leaf-only; the root's hush command on the desktop is
removed after the key ceremony. Joins signed by the desktop's intermediate
keep counting until it expires or the root revokes it.

### Workload identity

- **Node to spain.** A node's hush that asks spain's hush for a credential
  (stage 6) is identified by its troupe node key: mTLS with its leaf, the
  same check the figaro node door does. Over the tailnet, spain may also ask
  tailscaled `WhoIs` for the peer and require that the tailnet node key
  matches the one pinned for that node at join. Never a raw IP: an address
  is a lookup key for `WhoIs`, never an identity.
- **Sandbox, locally.** A local service (hush, the egress proxy, the valet)
  identifies a sandbox by the cgroup of its peer: `SO_PEERPIDFD` (Linux 6.5)
  or `SO_PEERCRED`'s pid, then `/proc/<pid>/cgroup`, mapped to the unit
  (`systemd-nspawn@shell0.service`, `figaro-valet@gluck.service`) and from
  the unit to the lease and the figaro. The pidfd closes the pid-reuse race.
  sandbox-node's proxy does the uid half of this today.

## NixOS module surface

House mode lands on NixOS hosts only: spain today, Merlin (Gluck's desktop
after the cutover) next. See [Hosts and carriers](#hosts-and-carriers).

### `services.figaro` (figaro's `nix/module.nix`, extended)

| option | default | what it does |
|---|---|---|
| `enable`, `package`, `user`, `uid`, `gid`, `stateDir`, `runtimeDir`, `settings`, `providers`, `outfits`, `toolPackages`, `memoryMax` | as today | unchanged |
| `owner` | required | principal written into the generated policy as the owner, e.g. `"user:1000"` |
| `users` | `[ ]` | local users who may reach the CLI door; added to group `figaro-door` and to `[authz.peercred] uids` |
| `socketActivation` | `true` | the angelus takes its listeners from systemd instead of binding them |
| `hush.enable` | `config.services.hush.enable` | renders `[cred] socket = "/run/hush/cred.sock"` for every provider in `providers` that names an instance |
| `valet.users` | `[ ]` | per user, a box `valet-<user>` with its exec and door paths and `figaro_claim = "required"` |
| `localTools` | `false` | allow `system.sandbox = "local"`; false refuses it in house mode |

Units and sockets:

- `figaro.socket`: `ListenStream=/run/figaro/angelus.sock`, `SocketUser=figaro`,
  `SocketGroup=figaro-door`, `SocketMode=0660`; one more `ListenStream` per
  configured web door. `figaro.service` gets them by `LISTEN_FDS`, by name
  (`FileDescriptorName=`).
- `figaro.service`: as today, plus `Requires=figaro.socket`. It needs no
  extra group for hush: the cred socket is group `figaro`.
- Box door sockets stay bound by the angelus (it must see which socket a
  call came through), under `/run/figaro/boxes/<box>/`, group-owned so the
  valet of that user can bind it into its fence.

### `services.hush` (hush's `nix/cred-module.nix`, renamed and extended)

| option | default | what it does |
|---|---|---|
| `enable`, `package`, `recipient`, `identityFile`, `instances`, `egress.*` | as `services.hush-cred` today | unchanged |
| `user` / `uid` | `"hush"` / 476 | was `hush-cred` |
| `credUsers` | `[ "figaro" ]` | uids admitted on the cred socket; replaces the single `allowUser` |
| `dropUsers` | `[ ]` | users put in `hush-drop`, who may sign in and push |
| `upstream` | `null` | stage 6: spain's hush URL on the tailnet; the node's hush then asks it instead of minting |
| `serveNodes` | `{ }` | stage 6, spain only: node id to the instances that node may get |

Units: `hush-cred.socket` (cred), `hush-drop.socket` (drop box),
`hush-admin.socket`, one `hush.service` taking all three by name;
`hush-fence.service` as `hush-cred-fence` today.

### `services.figaro-valet` (new, in figaro's flake)

| option | default | what it does |
|---|---|---|
| `users` | `[ ]` | users who get a valet |
| `maxGrant` | `"7d"` | longest grant the valet accepts |
| `fence` | `"go"` | `"go"` (userns, mount namespace, Landlock) or `"bwrap"` |

Units: `figaro-valet@.socket` with `ListenStream=/run/figaro-valet/%i/exec.sock`,
`SocketUser=%i`, `SocketGroup=figaro`, `SocketMode=0660`;
`figaro-valet@.service` with `User=%i`, state under the user's
`~/.local/state/figaro-valet`, and an admin socket in the user's runtime
dir, 0600.

### Users and groups

| name | kind | members |
|---|---|---|
| `figaro` (2001) | system user and group | the angelus; the group owns the cred socket and every valet exec socket |
| `hush` (476) | system user and group | hush |
| `figaro-door` | group | humans in `services.figaro.users` |
| `hush-drop` | group | humans in `services.hush.dropUsers` |

### Hosts and carriers

El Mayor's ruling, 2026-11-17: the root half ships through NixOS only.
Gluck's desktop moves to Merlin (NixOS) at the cutover, where the charter's
`my.figaro.mode = "system"` already maps to `services.figaro`. That leaves
cheroot as the only Arch host, too few for a root tier: no numtide
system-manager tier (a second root-activation carrier for one laptop) and
no pacman package of units (units and users outside Nix drift from the
charter). cheroot stays in home mode, which is a supported, tested mode and
not a fallback. The charter gains `my.hush.mode = "system"` and
`my.figaro.valet.users` beside the existing `my.figaro.mode`.

## Staged plan

Each stage ships alone, is reversible, and names what breaks.

| stage | what | on disk | in the protocol | reverse |
|---|---|---|---|---|
| 1. hush as a system service | rename `services.hush-cred` to `services.hush`, user `hush`, `credUsers` list, the drop box. figaro providers on house hosts use `[cred] socket` (already shipped) | spain: `/var/lib/hush-cred` moves to `/var/lib/hush`, uid unchanged at 476, user renamed; rou2 is the one consumer and follows the new socket path | new drop box routes; `hush cred login` run as the minter user via sudo is replaced by `hush login` over the drop box | rename back; files are the same format |
| 2. socket activation for the angelus | the angelus takes named listeners from systemd; `figaro.socket` in the module | none | none; the CLI dials the same path | `socketActivation = false` |
| 3. the valet in home mode | `figaro-valet`, the grant table and CLI, the Go fence; opt in with an outfit naming `valet-<user>` | new `~/.local/state/figaro-valet` | none in figaro: it is a box | stop using the outfit |
| 4. house mode on Merlin | `my.figaro.mode = "system"` with `owner`, `users`, `valet.users`; default outfit sandbox `valet-gluck`; `localTools = false` | the store moves from `~/.local/state/figaro` to `/var/lib/figaro`, chowned to `figaro`, by a one-shot import; `~/.config/figaro` is replaced by the rendered config | the CLI no longer spawns; any figaro on `local` is refused at open with "local tools are off in house mode"; a policy with no owner binding is refused at load | stop the unit, chown the store back, home mode again |
| 5. spain closes U1 | spain's unbound figaros get a sandbox (a shell class or the valet's spain equivalent); `localTools = false` on spain | none | spain figaros on `local` refused; `herald-decide` (no tools) unaffected | `localTools = true` |
| 6. hush federation and the key ceremony | spain's hush serves node hushes on the tailnet, mTLS with troupe leaves, `WhoIs` node-key pin; nodes set `upstream`. Root to offline media; desktop intermediate retired | nodes stop holding refresh tokens: their `/var/lib/hush` instances are deleted after the switch; desktop `troupe/<name>/intermediate.*` removed | new hush-to-hush API; new ledger field pinning a node's tailnet key | nodes drop `upstream` and sign in locally again |
| 7. remote BYOS | the phase 2 figaro line: an exec transport over the node door carrying `Figaro-Id`, so a box's `exec` may be `node:<id>/<class>`; sandbox-node accepts it after mTLS | none | new: `box.*` over the node door, refused unless the caller is a troupe node the provider's config names | remove the box entry |
| 8. grant mirrors | `system.mirror.<enforcer>`, writable only by that enforcer's principal; the valet and hush write theirs | new keys on forms | new protected key class | unset the keys; no enforcer reads them |

Stages 1 to 3 need nothing from each other and can go in parallel. Stage 4
needs 2 and 3. Stage 6 needs 1. Stage 7 needs only the troupe.

## Open for Gluck

1. The valet's grant gesture: CLI only from the user's own shell (proposed),
   or also a passkey page like the spain grants design.
2. Whether a grant may name a lineage root (cover every descendant) or only
   one figaro id. Proposed: lineage allowed, since a child can never exceed
   its parent's scopes anyway, but each child's use is logged by its own id.
3. The derived-roles question from `comment_de2faac1a3b9` ("hooks and
   derivations run externally") touches how the mirror keys are written;
   this proposal assumes the enforcer writes them directly.
