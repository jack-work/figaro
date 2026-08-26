# The HTTP gateway — figaro as a mesh of peers

Status: proposed (rev 2)
Branch: `feat/http-gateway`

Rev 2 replaces the "CLI dials a remote" design of rev 1 with a **peer
architecture**: there is no server and no client, only angelus nodes that may
own arias and may federate with each other. The CLI stays dumb.

## 0. The shape

```
  figaro CLI ──unix socket──▶ LOCAL ANGELUS (a peer)
                                   │
        ┌──────────────────────────┼───────────────────────────┐
        │                          │                           │
   owns local arias        holds peer credentials      knows the ring
                              (hush, kept warm)      (who owns which aria)

   CONTROL PLANE                          DATA PLANE
   ───────────────                        ──────────
   list, status, topology, resolution     aria streams, form deltas,
   ROUTED THROUGH the local angelus       qua, set, interrupt
   to peers, and merged.                  DIRECT to the owning node's
                                          gateway. One hop, no relay.
```

Three rules that follow, and everything else in this document is downstream
of them:

1. **The CLI never speaks HTTP.** It has one transport: the local unix
   socket. It holds no credentials, resolves no origins, and knows nothing
   about remotes. Every remote capability arrives as a richer answer from the
   local daemon, never as a new thing the CLI must do.
2. **Control-plane requests route through the local daemon.** `figaro ls`
   asks the local angelus, which fans out to peers and merges. This is what
   makes registry federation possible without the CLI learning about it, and
   what a future consistent-hash ring slots into unchanged.
3. **Data-plane traffic goes direct to the owning node.** Once the local
   daemon knows node N owns aria X, the connection for X's stream, form
   deltas, and turns is opened straight to N's gateway. Relaying high-volume
   streams through the local daemon would double every hop and make the local
   node a backpressure choke point.

### Why this is better than rev 1

- **Credentials live in exactly one place.** The daemon holds them in `hush`
  and keeps them warm. No CLI invocation ever touches a token, so no token
  reaches a process table, a shell history, or an env var.
- **Attendance stays meaningful.** `pid.bind` is about *this machine's*
  processes. In rev 1 it was nonsense against a remote daemon; here it never
  leaves home.
- **The registry merges naturally.** `figaro ls` over a mesh is one local
  call whose answer happens to span nodes.
- **It is already the end state.** Single-hop consistent hashing needs every
  peer to hold the ring and resolve owners locally. That is exactly the
  control/data split above, with the resolution step made cheap.

### The direction of travel

Eventually the ownership database is distributed across peers by consistent
hashing, with every node holding the full ring — so any node resolves any
aria's owner without a lookup round trip, then connects in **one hop**. The
local CLI daemon is a participant in that ring like any other: it may host
arias, or host none and act purely as a client-participant. **There is no
server.** What this codebase occasionally calls "the server" is a peer that
happens to own more.

### Node identity and credentials (rev 3)

Angeli resolve **to each other over HTTPS**, and every call between them is
authenticated. That needs a notion of node identity figaro does not have yet:

- **App registration.** A node is configured with a secret and an identity.
  The credential it presents **encodes the figaro identity on whose behalf it
  is calling**, so a remote node can authorize *different figaros
  differently* rather than treating a whole peer as one principal. This is
  the OAuth client-registration shape: node = client, figaro = subject.
- **Short-lived bearer tokens.** The registration secret is the *long-lived*
  credential and never travels on a request. It mints **short-TTL bearers**,
  which are what the wire carries. This is the answer to the security
  review's "no revocation on long-lived connections": a token that expires in
  minutes bounds a leak without needing a revocation channel.
- **A static entry point.** Remote configuration matters *even when a local
  figaro is running*, because the useful shape is "connect to a remote figaro
  endpoint that resolves to one gateway angelus on public DNS." That node is
  a rendezvous, not an owner: it answers *who owns what* and the caller then
  goes direct. It is the ring's bootstrap node.

**Why this must wait for sandboxing to be *useful*, but not to be *built*.**
Per-figaro authorization across nodes is exactly the "differentiated
privilege" the doorkey argument said needs containment first. Building the
credential *plumbing* now is fine — it is inert until a policy keys on it.
Building a policy that *grants agency* to a subject on the strength of it is
not. The plumbing is rev 3; the grants are post-sandbox.

### Containerization

The target deployment is one node per container: NixOS systemd-nspawn,
compose-style actors, or k8s. Two constraints this puts on everything above:

- **No ambient local state in the trust story.** A node's identity comes from
  injected configuration (`LoadCredential`, a mounted secret), never from
  "you had to be on this machine." The unix-socket argument that makes rev 3
  step 1 safe does *not* survive containerization, so the network path must
  be genuinely authenticated before a container ships.
- **The angelus must be a foreground process** with no detaching grandchild.
  `ensureAngelus` forks a detached daemon, which under `Type=exec` escapes
  the supervisor. Containers need `figaro --angelus` as PID 1 of its unit and
  `figaro serve` as a sibling with `BindsTo=`.

> **Open for confirmation.** "Aria/form APIs come directly from the node" —
> I read *directly* as contrasting with *through the remote angelus's control
> plane*, with the connection still opened by the **local daemon** on the
> CLI's behalf (since the CLI stays dumb). The alternative reading is that
> the daemon hands the CLI a redirect and the CLI connects itself, which
> contradicts rule 1. Proceeding on the first reading.

## 1. Where the code lives

`internal/gateway` — the HTTP face of a peer, started by `figaro serve`.
Not a separate process role: **any angelus can expose itself.** `serve` says
"be reachable," not "be a server."

The governing constraint survives from rev 1:

> **Never retype the contract.** Share `api/rpc`, transport `rpc.*` as raw
> JSON, and where parsing can be avoided, avoid it.

## 2. The tunnel face

`GET /v1/socket` — WebSocket upgrade, then bytes are copied between the
socket and the peer's angelus. The gateway does not parse what it carries.

**Validated**: `internal/gateway/tunnel_test.go` drives an *unmodified*
`jkrpc` client through a real websocket against a real jkrpc server —
calls round-trip, notifications arrive in order, 512 KiB frames survive the
line reader, and a policy refusal does not sever the connection.

### The attribution correction (from security review)

Rev 1 claimed both "forward the original bytes verbatim" and "enforce
policy." Those are in tension, and the tension is real: `authz.AriaHeader`
trusts `x-internal-figaro-id` straight out of params, so **a remote caller
could name itself any aria** and impersonate the DUKE in a transcript.

Resolution — a distinction rev 1 failed to draw:

- The **payload** is never re-encoded.
- The **attribution envelope** is always rewritten. On ingress the gateway
  decodes params as `map[string]json.RawMessage`, deletes every caller key
  (`rpc.CallerKey`, `x-caller`, `sender`), inserts the identity *it*
  authenticated, and re-encodes. Unknown fields survive byte-exact as
  `RawMessage`; only key order changes, which carries no meaning.

Rewriting the envelope is not retyping the contract. Say it once, here.

## 3. The form face

REST + SSE for browsers, on the owning node.

```
GET   /v1/forms/{id}                  → {snapshot, version}   ETag: "42"
PATCH /v1/forms/{id}                  → message.Patch, If-Match → 200 | 412
GET   /v1/forms/{id}/deltas?since=42  → SSE; id: <version>
```

### The CAS mapping

figaro's CAS: a linearizable, whole-form, exact-match optimistic lock on a
contiguous WAL index, with value-idempotent reduction (`store.reduceOne`).

| figaro | HTTP |
|---|---|
| `version` | `ETag: "42"` |
| `if_version: 42` | `If-Match: "42"` |
| `ErrFormMoved` | `412` |
| `outcome: applied` | `200` + new ETag |
| `outcome: unchanged` | `200` + unmoved ETag (**not** 304) |
| `assert` on absent key | `409` |
| `CheckWritable` refusal | `403` |
| sealed / tombstoned | `410` |

**Confirmed by reading the store**: `AppendPatch` returns the form channel's
WAL append index, and an empty patch never appends — so versions are
contiguous and `Last-Event-ID` resume is sound.

**But history is trimmable** (`trimPatches`, `st.trimmed`). So SSE must have
two event types:

- `event: delta` — `id: <version>`, one `rpc.FormDelta`.
- `event: reset` — the full snapshot and its version, sent when `since` is
  older than retained history. This is the honest answer to a gap, and it is
  the same thing `formMirror.resync` does today.

Rules that are not negotiable: no ETag on a sub-resource (the lock is
whole-document); `unchanged` is 200, not 304; own media type
(`application/vnd.figaro.form-patch+json`) because merge-patch's `null`
means delete and a form value may legitimately *be* `null`.

**Connection pooling** (settles rev 1's open question 2): `figaro.set` and
`form.delta` are *not* served on the angelus door — only per-aria hub
sockets carry them. So the gateway holds **one pooled upstream connection
per aria**, shared by every HTTP session watching that form, with per-session
buffered fan-out. This is also the fix for `hub.Notify`'s synchronous
per-conn write: one slow browser can no longer sit on the daemon's notifier
goroutine.

## 4. Security: what review found, and what changes

Five CRITICALs came back. They are recorded here because most of them are
**not gateway bugs — they are pre-existing holes the gateway would expose.**

### C1 — the policy factory fails open

`handlers.policy()` is `switch name { case "default": …; default: AllowAll }`
and `AuthzPolicy()` passes unknown names through. A config saying
`policy = "rules"` against a binary that does not know that name yields
**allow-all**. Stacked with rev 1's "run the filter only when policy is not
allow-all", a typo produced unauthenticated RCE.

- Unknown policy name → **refuse to start**, in config validation.
- The filtering pump runs **always** on the gateway path. `allow-all` is a
  policy the pump consults, never a reason to skip it.

### C2 — `authz.Rules` is allow-by-default

`Rules.Check` returns `Allow()` when nothing denies, and a `Rule` can only
deny. Deny-by-default grants are **not representable** in the existing seam —
rev 1 was wrong to say the seam already existed. Need a distinct `Grants`
policy whose **zero value denies**, a fatal parse error, and a test asserting
`Grants{}.Allow(anything) == false`.

### C3 — the agency methods are not guarded at all

`authz.Guard` wraps only the angelus map. `figaro.qua`, `figaro.set`,
`figaro.study/cast/drop`, `figaro.interrupt`, `figaro.queue.*` are served on
**per-aria hub sockets** with no policy whatsoever. Rev 1's method table
named methods the angelus door never carries.

**This blocks everything else.** Move the guard below hub dispatch so the
guarded set equals the served set by construction, and add a test that
enumerates every registered method on every socket and fails on any method
with no policy class. `figaro.attach` needs particular care: it hands back
an aria's unix socket path, i.e. a route to the unguarded surface.

### C4 — WebSocket CSRF with an ambient credential

CORS does not apply to WebSockets. Behind Authelia the session cookie is
ambient, so any page the operator visits can open
`wss://fig.kelliher.info/v1/socket`, have the browser attach the cookie, and
speak raw JSON-RPC.

- Mandatory `Origin` allowlist on `/v1/socket`; **deny on mismatch and deny
  on absent-Origin-with-cookie** (a native client sends neither).
- `Sec-Fetch-Site` check on PATCH; Authelia cookie `SameSite=strict`.
- Never accept `?token=` in a query string — it lands in access logs,
  `Referer`, and history.

*(Already implemented: origins default to empty, which refuses every browser
origin — `TestTunnelRefusesBrowserOriginByDefault`.)*

### C5 — the refusal table must be a table

Rev 1 refused `upstream` on a public bind but said nothing about `none`,
which is worse, and is what step 1 of the build order tells people to copy.
One refusal table keyed on **(authn × bind reachability)**, every cell
unit-tested, `none` and empty-doorkey as hard denials.

And the loopback test cannot be a string check: `:9090`, `::ffff:127.0.0.1`,
a hostname resolving to a LAN address, `docker -p`, `ssh -L` all defeat it.
**Bind first, then test `listener.Addr()`'s resolved IP** with `IsLoopback()`;
refuse `0.0.0.0`/`::`/empty host outright; add a `Host:` allowlist so DNS
rebinding fails even on loopback. Container and port-publish escapes are not
detectable — document that, and require the doorkey there.

### What the peer model changes

The mesh makes the trust story *cleaner*, not harder: peer-to-peer links are
**machine-to-machine**, so they take a doorkey or mTLS, not an ambient
browser cookie. The cookie path exists only for the form face. C4 shrinks to
the browser surface, and the CLI path — the one that matters for daily use —
never involves a cookie at all.

## 5. Authentication

Unchanged in posture, narrowed in scope. The gateway authenticates a **peer**
or a **browser**; it never mints figaro identities. That waits for
sandboxing, because a figaro identity is a handle on an agent that runs
`bash`, and differentiated privilege without containment is a lie.

| name | reads | for |
|---|---|---|
| `upstream` | `Remote-User`, `Remote-Groups` | browsers behind Authelia |
| `doorkey` | `Authorization: Bearer` from `hush` | **peer-to-peer links** |
| `none` | nothing | development, loopback only |

The doorkey is the peer-link credential and is legitimate without sandboxing:
it makes no privilege distinction between callers trusted differently. The
test for any future credential stands — *does it distinguish two callers I
trust differently? No → doorkey. Yes → sandbox first.*

`upstream` must not trust `Remote-*` on faith: pair it with a preshared
gateway key that only the proxy holds, since the header stripping lives in
another repo and anything reaching the port can otherwise claim
`figaro-admin`.

## 6. What the CLI gains (without getting smarter)

No `--origin`. No credentials. Instead:

```sh
figaro peer add spain https://fig.kelliher.info   # daemon stores key in hush
figaro peer ls                                    # daemon answers
figaro ls                                         # merged across peers
```

`figaro peer *` are ordinary RPCs to the local daemon. The daemon dials, the
daemon holds the token, the daemon merges. `rpc.FigaroInfoResponse` grows a
`node` field so a listing can say where an aria lives.

The `https` transport built in `api/transport` stays — but its **caller is
the daemon**, not the CLI. That is the one thing rev 1 got backwards.

## 7. Deployment on spain

Ops review found §8 of rev 1 to be "one paragraph of aspiration standing on
four load-bearing assumptions the stack does not honor." Corrections:

- figaro is **not** a Python web app. It needs `HOME`, a writable store,
  `XDG_RUNTIME_DIR`, provider credentials via `hush`, and network egress.
  `DynamicUser` + `StateDirectory` does not fit unmodified.
- Two units: `figaro-angelus.service` (owns `RuntimeDirectory=figaro`,
  `StateDirectory`) and `figaro-gateway.service` with `BindsTo=` +
  `After=` — `BindsTo` so a dead angelus takes the gateway down rather than
  leaving it 502-ing.
- Secrets arrive as `LoadCredential` from sops, surfaced as `*_file` config
  keys. **Blocking figaro-side work**: `doorkey_file`,
  `FIGARO_HUSH_PASSPHRASE_FILE`, per-provider `api_key_file`.
- Confirm WebSocket and SSE survive Cloudflare tunnel + Caddy timeouts.

## 8. Build order

**Step 0 blocks everything**: C3. Until the guard sits below hub dispatch,
there is no point gating a door into an ungated house.

- [x] **0. Guard below hub dispatch.** `ariaHub.guard`, set by `hubFor`.
      The agency methods — `qua`, `set`, `study`, `cast`, `drop`,
      `interrupt`, the queue verbs — were served with no policy at all.
      Enumeration test so a new method cannot slip through.
- [x] **1. Fail closed.** `authz.Grants` (zero value denies), unknown policy
      name refuses to start (`config.ValidateAuthz`, called before the lock
      in `runAngelus`). The test that asserted the old fail-open is rewritten
      to assert the refusal.
- [x] **2. `figaro serve`, unix socket only.** A `tcp://` address is refused
      rather than downgraded. Browser origins denied by default.
- [ ] 3. The refusal table: (authn × bind reachability), every cell tested;
      bind-then-inspect `listener.Addr()`; `Host` allowlist (C5).
- [ ] 4. Envelope rewriting on ingress; `upstream` + `doorkey` authenticators.
- [ ] 5. Form face: GET/PATCH/SSE, pooled per-aria connections.
- [ ] 6. Wire gaps: `SubscribeFrom` on the wire, typed `ErrVersionConflict`,
      golden vectors.
- [ ] 7. Node identity: app registration, short-TTL bearers.
- [ ] 8. Peer registry in the daemon; federated `figaro.list`.
- [ ] 9. Direct data-plane connections to the owning node.
- [ ] 10. Nix module; deploy on spain; container topology.

### What shipped in the first increment, and why it is the minimal safe one

Steps 0–2 are in. The security argument for stopping exactly there:

- A **unix socket is the trust model figaro already has.** The angelus socket
  is 0600 and rests on "you had to be me to reach it." The gateway socket
  inherits that argument unchanged and adds nothing new to trust.
- **No TCP listener means the network findings are unreachable, not
  unsolved.** Loopback detection, DNS rebinding, `X-Forwarded-For`, header
  smuggling — none of them apply to a door with no port. That is a much
  stronger claim than having fixed them.
- It is **deployable behind Caddy today** (reverse proxies speak to unix
  sockets), so spain is reachable without figaro opening a port itself.
- And it closes **three real pre-existing holes** on the way: the ungated
  agency surface, the fail-open policy factory, and the un-representable
  deny-by-default.

The next increment that changes the trust story is step 3, and it must not
land without its table of tests.

## 9. Testing

- Every cell of the refusal table (C5).
- `Grants{}` denies (C2); unknown policy name refuses to start (C1).
- The method-class enumeration test (C3) — it must fail when someone adds a
  method and forgets to classify it.
- Tunnel: unmodified SDK over websocket. **Done and passing.**
- Envelope rewriting: a frame naming another aria arrives attributed to the
  authenticated caller instead.
- End to end: CLI → local daemon → peer → back.
