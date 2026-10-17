# 2026-10-17: the door, the ACL, sandboxes and federation

**Status:** decided with the owner in aria `d9a9cebf` (design turn plus form
comment threads, 2026-10-16). Supersedes §2 and §3.3 of
`2026-10-12-terraform-sandbox-federation.md` where they disagree. Driven by the
Impresario role; ledger at `~/.local/state/impresario`.

## Goal

figaro runs on spain in an nspawn container with its own Authelia identity.
Sandboxes (nspawn units from flake templates, egress but no owner credentials)
run tools for arias and may call figaro back. Several nodes pool their arias.
Every hop is authenticated; every service authorizes its own resources.

## Layers

| layer | decided |
|---|---|
| identity | Authelia is the only issuer (option A). A sandbox's Authelia client secret is held by the sandbox service on the host, never in the box; the box gets short-lived JWTs from a per-sandbox token socket. Subjects are SPIFFE-shaped so SPIRE can replace the issuer later. |
| authn | at the edge. kelliher-web's gateway verifies an Authelia session or a bearer JWT, strips client `Remote-*`, stamps user, groups and principal kind, and forwards the original token. figaro also authenticates natively (peercred, upstream headers from loopback, JWT, mTLS between nodes) so it works without the platform. |
| authz | in each service. Tokens carry audience and coarse scopes; roles live in each service's config: principal -> roles -> verbs on selectors. One shared Go library holds principals, the JWT verifier, the RBAC grammar and reload. |

## figaro's ACL

- Principals are stamped by the authenticator, never read from params.
  Kinds: `user`, `sandbox`, `node`. A sandbox, not an aria, is the principal
  for anything running in a box. Many arias share one sandbox. The calling
  aria id is attribution only: rendered as a label, forwarded on outbound
  calls, never used for a decision.
- Every RPC method is classified into one verb: `read`, `listen`, `send`,
  `fork`, `set`, `create`, `kill`, `admin`. An unclassified method fails a test.
- Selectors: `aria:<id>`, `role:@name`, `owner:self` (arias this principal
  minted; owner is server-stamped at birth), `lineage:<id>`, `*`.
- Roles are named bundles of verbs on selectors, bound to principals.
- Policy is TOML in the config dir, reloaded like outfits; on spain it is
  generated from the flake. A failed reload keeps the old policy. The default
  local policy makes the daemon's own uid owner of everything, so a desktop
  behaves as today.
- The stamped principal is recorded on every user tic.

## The door

One multiplexed listener replaces the per-aria sockets (`figaros/<id>.sock`).
The aria id travels in the request. The same door serves unix and TCP/TLS.
The CLI resolves a daemon endpoint the way it resolves a model provider:
a configured target with an auth chain (peercred locally, a workload token in a
box). With an endpoint configured the CLI never spawns a daemon.

## Credentials on servers

A credentials-directory strategy (`$CREDENTIALS_DIRECTORY`, systemd
LoadCredential, sops-nix upstream). hush is constructed only when a strategy
needs it. No vault for now.

## figbox

A generic sandbox agent serving `api/sandbox` (exec, sessions, fs) over its
ingress socket, figaro-agnostic. Its ingress policy (principal -> verbs with
path scopes) uses the shared RBAC grammar, comes from the template in the flake,
and is mounted read-only. The daemon checks which aria may use which sandbox
(a `use` grant); figbox checks which callers the template admits.
**Not started until the owner's go-ahead** (roadmap S*).

## Egress

Own services receive the token directly and authorize. Third parties go through
a credential-injecting relay: the core of `figaro serve` extracted (proposed as
`hush relay`), with `figaro serve` rebuilt on it. **S3, held.**

## Federation

- Each store gets a UUID: the node id.
- New aria ids are 128-bit, shown as short prefixes. Existing 8-hex ids are
  never rewritten; a collision found when a peer joins is resolved by node
  qualification (`node/id`) for that id only.
- Directory by home node: an aria lives where it was minted. Each node keeps a
  cache of remote owners filled by births, `ls` fan-out and lookups, and asks
  every peer on a miss. Placement sits behind a directory interface so a hash
  ring can replace it later.
- One hop. Membership is static (from the flake). A client request may be
  forwarded once; a forwarded request (flag set only by a node principal) is
  served locally or refused. An unreachable owner is an error naming the node.
  `ls` returns local rows plus a per-node unreachable marker.
- The entry node proxies everything, `listen` included. The caller's JWT is
  forwarded; its audience is the federation, and the owner re-verifies it.
  Nodes never vouch for callers.
- Forks stay on the parent's node.
- Migration (F3) changes an aria's home node, updates the registry on every
  node and leaves a forwarding tombstone. Lineages move with their shared
  prefix and translator caches.
- The second node for testing is a qemu VM on spain, deployed with deploy-rs.
  gluck joins later, once it is infra.

## Roadmap

```
0a 0b 0c ─┬─ M1 ─ M2 ─ M3 ─ M4
          │         └──────── F1 ─ F2 ─ F3
          ├─ E1
          └─ S1 ─ S2 ─ S3   (held)
```

| id | what | where |
|---|---|---|
| 0a | this note | figaro |
| 0b | shared authz library | new repo `jkauthz` |
| 0c | `api/sandbox` as its own module | figaro |
| M1 | one door | figaro |
| M2 | authenticators and ACL | figaro, jkauthz |
| M3 | server credentials, CLI endpoints | figaro |
| M4 | figaro in nspawn on spain | spain-flake, figaro `nix/` |
| E1 | edge verifier | kelliher-web |
| F1-F3 | node id, peering, migration | figaro, spain-flake |
