# Track K: passkey grants, suzerainty, the escape hatch

Status: design note, step 1 of Track K. Nothing is built. Base: keel main `fb5b001f8` (v0.77.4).
Lane index `fleet.lane.figaro.track-k`; plan `2026/11/19/unified-plan-20261119.md` (captains-log).
Grounded in keel `lane/approvals-root` 7b31bf9f2 (live at approvals.kelliher.info),
`lane/king-authority` 2e922c488 and `lane/king-delegate` dc14dd462, none of them in main yet.

## Where things stand

- **Approvals** (`internal/approvals`): a figaro files `elicit.<id>` on a role form; the server
  builds a plan from a fixed template (`figaro.model`, `figaro.ttl`, `role.model`), shows it with
  its sha256, rebuilds it on Approve and writes with `IfVersion`. The server runs outside any
  figaro, so the daemon judges it the owner. Gluck is proved by Authelia (`user:admin`,
  two_factor) at the edge plus an edge secret. No step-up, no passkey, no process started
  (a test fails if the package imports `os/exec`).
- **King authority**: the King seats, casts and retires inside its subtree; `child_policy`,
  scopes, fan-out and quota are bounded by what the writer holds.
- **King delegation**: owner-held `system.delegate` (a list drawn from `system.model`,
  `system.provider`, `system.fanout.max_children`, `system.fanout.quota`) and
  `system.delegate_roles` on a role form let its holder write those keys on its subtree and on
  listed roles. One choke point, `handlers.delegation` in `internal/angelus/delegate.go`. No
  expiry, no revocation other than unsetting the key, and provider and model are delegable with
  no click at all.
- **Home mode** (gluck today, `figaro doctor mode`): figaros run as gluck, and a figaro with a
  shell can reach the daemon as the owner (#57). peer_check raises the bar; it does not close it.
  Root is a real wall on gluck (`sudo -n` refuses figaros). This note therefore puts every wall
  that must hold against a compromised King behind root, and says where a rule is only a fence.
- `projects.deployment_grants` on the King's form: a "future" text, never built. This note
  replaces it.

## 1. Passkey step-up and the digest-bound grant

### The grant

A grant is one WebAuthn assertion whose challenge is the plan digest. Nothing else is issued:
no token, no session, no bearer. The assertion is self-verifying, so the verifier needs only the
enrolled public key and the plan, and need not trust the approvals app, Authelia, spain or the
gluck uid.

1. **File.** A figaro files `elicit.<id>` with a new verb, `root.run`:
   `{verb, host, argv[], base, candidate, expires, reason}`. `argv` is a list, never a shell
   string. `base` is what the host runs now (flake rev or generation), `candidate` what it will
   run. `expires` is at most 1h out for root.
2. **Plan.** The server validates as today (unknown fields, non-ASCII, control or bidi bytes,
   an element over 4 KiB: Deny-only) and builds the plan
   `{filing, filing_version, id, verb, host, argv, base, candidate, nonce, epoch, expires}`.
   `nonce` is 16 random bytes the server mints at render; `epoch` is the hatch epoch (section 3).
   Digest = sha256 over its canonical JSON.
3. **Show.** The page shows the whole command: host, user root, every argv element on its own
   line, untruncated, base and candidate with a keel diff link between them, the expiry and the
   digest. A command too long to show is refused, never elided. `reason` stays under "Filer's
   words, unverified".
4. **Sign.** Approve calls `navigator.credentials.get` with `challenge = digest`,
   `rpId = approvals.kelliher.info`, `userVerification = "required"`. The authenticator signs
   clientDataJSON, which carries the challenge.
5. **Run.** The server hands `{plan, assertion}` to the executor on that host (below). It does
   not run anything itself; the `os/exec` test stays.

### The executor

`figaro grant serve`, a root unit per node, socket-activated. On gluck it listens on a root-owned
unix socket, and answers hatch status (read-only) on the tailnet; on spain it listens on the
tailnet. Since the grant is self-verifying, who submits it does not
matter. It runs a plan only if all hold:

| check | refuses |
|---|---|
| assertion verifies against `/var/lib/figaro-grant/credentials.json` (root, 0600) | forged or foreign grant |
| clientDataJSON challenge equals the digest it recomputes from the plan | shown is not run |
| origin `https://approvals.kelliher.info`, type `webauthn.get`, UV flag set | phishing, presence-only taps |
| digest not in `/var/lib/figaro-grant/used` (append before run) | replay |
| `expires` in the future, `host` is this node | stale or misrouted grant |
| `argv[0]` and its shape are in the root-owned allowlist `/etc/figaro/grant-commands.toml` | general sudo |
| host's current rev equals `base` | fresh-base: a plan approved against yesterday's system |
| plan `epoch` equals the current hatch epoch and the hatch is open | revoked delegation |

It runs `argv` with no shell, a fixed environment and no stdin, then writes a receipt (time,
host, digest, argv, base, rev after, exit status) to the journal and to a root-owned log, and
answers it to the server, which removes the request with `IfVersion` as today.

Credentials are enrolled only at the owner terminal: `sudo figaro grant enroll` serves a one-shot
registration page on loopback; no flake, form or figaro writes `credentials.json`. Synced
passkeys report a sign count of 0, so the count is checked only when non-zero.

### Batches (folds `projects.deployment_grants`)

A batch is an ordered list of `root.run` plans, digest over the list of item digests. One
ceremony approves the list; the executor runs items in order, stops at the first failure, and
marks the batch used as a whole. The page shows every item in full. This is per-item and
explicit batch approval, digest-bound, single-use, through a constrained executor with fresh-base
checks and receipts: the whole of `deployment_grants`. Delete the key from the King's form when
the batch slice lands.

### Passkeys for form templates

`figaro.model`, `role.model` and the delegation template below carry the same ceremony, verified
by the approvals server itself. In home mode that guards against a stolen Authelia session, not
against a local figaro, since the server runs as gluck. In house mode the daemon verifies the
assertion, with the credential in its own state, and it becomes a wall.

### Where the passkey lives

| | Authelia step-up (OIDC, `max_age=0`, `nonce = digest`, `amr` must hold `hwk`) | In the approvals app (RP `approvals.kelliher.info`) |
|---|---|---|
| what the authenticator signs | Authelia's challenge; the digest rides in Authelia's signed ID token | the digest itself |
| verifier trusts | Authelia, its signing key, root on spain | the enrolled public key only |
| enrolment | the one Gluck already has | a second one, at the terminal |
| code | OIDC client (Llavero), token checks | a WebAuthn verify package, a small script on the page |
| offline from spain | no: an Authelia outage stops every grant | yes for gluck; spain grants need spain anyway |

**Recommendation: in the approvals app.** The unified plan leaned to Authelia for one place;
I depart from it. Root ops on spain are named by the King and run by El Zapador, so a King that
once held root on spain could mint Authelia tokens for every later grant. A passkey verified by a
root executor against a key enrolled at the terminal does not fall with spain. Authelia stays the
door (session, two_factor, edge secret); it does not become the signature. The cost is a second
enrolment and one more package.

## 2. Suzerainty

Owner-held keys and action classes, and who acts on them. "King" means the holder of a role
whose `system.delegate` carries the class, acting on its subtree or on `system.delegate_roles`.

| class | keys or action | King without click | passkey required |
|---|---|---|---|
| lifetimes | `system.ttl`, `system.keepalive`, `system.reap_if_empty` on descendants | yes, standing | |
| fan-out | `system.fanout.max_children`, `system.fanout.quota` inside the King's budget | yes (lineage today; delegation for listed roles) | beyond the King's own budget: never delegated |
| seats | seat, cast, succeed, retire in the subtree | yes (king-authority) | |
| scopes and child policy | `system.scopes`, `system.child_policy` up to what the King holds | yes (attenuated) | widening beyond the King: owner only |
| limits | `system.max_tokens`, `system.max_context_tokens`, `system.keep` | yes, standing | |
| models | `system.provider`, `system.model` | only under a standing grant naming values and an expiry | each change, or the standing grant itself |
| outfits | `system.grants.outfits` | no | yes, and only for managed, locked outfits pinned by digest |
| powers | `system.tools`, `system.environment.*`, `system.methods.*`, `system.credo`, `system.decide.base_url`, `succession-policy`, `lifecycle.status`, laws | no | yes, per change |
| root ops | `root.run` on any node | never | yes, per command or explicit batch; no standing form |
| delegation itself | `system.delegate`, `system.delegate_roles` | never | yes for a grant; revocation by the owner terminal or the hatch |

### How a standing class grant is held

`system.delegate` becomes an object of classes instead of a list of keys. Classes and their keys
are fixed in code (`form.DelegationClasses`), so a grant cannot name a key the table above keeps
back:

```json
"system.delegate": {
  "lifetimes": {"epoch": 7},
  "models": {"values": {"system.provider": ["anthropic"],
                        "system.model": ["claude-opus-4-5", "claude-sonnet-4-5"]},
             "until": "2026-12-04T00:00:00Z", "epoch": 7, "grant": "<digest>"}
}
```

- **Stored** on the role form, owner-held, so it survives succession with the seat and binds
  whoever holds it. Without-click classes are written once by the owner (terminal or managed
  apply). Passkey classes are written by the approvals server through a `role.delegate` template
  whose plan digest the passkey signed; `grant` records that digest for the audit.
- **Scoped** to the holder's subtree (`DescendsFrom`) plus `system.delegate_roles`, as in the
  lane, and for passkey classes to the listed `values`.
- **Expires** at `until`, required for passkey classes and at most 30 days out. The daemon reads
  a passed `until` as absent; no sweep is needed.
- **Revoked** by the owner unsetting the class, or by the hatch: a class whose `epoch` is not the
  current hatch epoch is void, so reopening the hatch never revives an old grant.

Breakage: `system.delegate` changes shape. Any role form carrying the list form fails its shape
and delegates nothing until rewritten. None is live; say it in the release note anyway.

## 3. The escape hatch

**Owner terminal.** Gluck's terminal stays the owner. Delegation only adds writers; it never
narrows the owner, so `figaro set`, `unset`, `hup`, `kill` and plain `sudo` keep working with
Authelia, spain, the approvals app and every passkey down.

**Kill switch.** `/etc/figaro/hatch`, root-owned 0644, `{"state": "open"|"closed", "epoch": N,
"at": ...}`. One owner command:

```
sudo figaro hatch close
```

It sets `closed` and bumps `epoch`. Its effects:

- the daemon's `handlers.delegation` returns false for every role while the hatch is not open,
  and also when the file is missing or unreadable (fail closed);
- every standing class grant carries an old epoch and is void, even after `hatch open`;
- every executor refuses every plan, and plans rendered before the close carry the old epoch;
- the spain executor asks gluck's executor for the hatch before each run and refuses when gluck
  does not answer. One command on gluck therefore closes both nodes.

The King cannot write it: figaros cannot sudo on gluck. In home mode a King with a shell could
still kill the daemon and run a patched one as gluck (#57): the hatch is then a wall for the
executors and a fence for the daemon. In house mode it is a wall for both. `sudo figaro hatch
open` bumps the epoch again; `figaro hatch status` needs no root.

**Sabotage test for step 3: `TestHatchCloseRevokesEveryDelegation`** (internal/angelus). A rig
King holds a role delegating `lifetimes` and a passkey-granted `models`; a test executor holds a
signed plan. After `close`, each of these must be refused: a delegated ttl write, a model write
inside the allowlist, the pre-close plan, the same grant after `open`. Each mutation must turn it
red: the hatch check removed from `delegation`; a missing file read as open; the epoch compare
dropped in the daemon; the epoch compare dropped in the executor; `open` not bumping the epoch.
Then the live drill on a disposable figaro (`--ttl 1h`, its own role form): its tool tries to
write `/etc/figaro/hatch`, `sudo -n figaro hatch open`, and a delegated write after close; all
three must fail, and the drill reports each refusal.

## 4. Slices

Each is one PR off main with `go test -race` on the touched packages and the sabotage named.

**Step 2: land what exists, made safe to land.**

| slice | content | tests |
|---|---|---|
| K2a | `lane/king-authority` rebased on main, after the Engineer's diff read of 1996c1555, a20e51cc2, 2e922c488 | `king_authority_test.go` as is |
| K2b | hatch reader only: `internal/hatch` (parse, fail closed, epoch) and the check at the top of `handlers.delegation`. No command yet, so no file exists and delegation stays inert | missing, unreadable, malformed, closed: each is closed; sabotage "missing reads open" reds |
| K2c | `lane/king-delegate` reshaped to classes: `form.DelegationClasses`, object shape, `until`, `values`, `epoch`; `lifetimes` and `limits` added; provider and model only as a values-and-until class | `king_delegate_test.go` plus expired, value outside list, stale epoch, list-shaped key refused; golden updated |

**Step 3: the grant path and the hatch.**

| slice | content | tests |
|---|---|---|
| K3a | `figaro hatch status/open/close` (root to write), unit docs | `TestHatchCloseRevokesEveryDelegation` with its five mutations; then the live drill |
| K3b | `internal/passkey`: registration and assertion verify (go-webauthn), `sudo figaro grant enroll` | wrong challenge, wrong origin, UV off, foreign credential, sign count regress: refused |
| K3c | `root.run` verb in `internal/approvals`, page and SPA ceremony (script-src 'self' on the routes that need it), no exec in the package | whole argv shown, overlong refused, bidi refused, digest changes with any field, `os/exec` test still green |
| K3d | `figaro grant serve` executor: allowlist, fresh base, used ledger, epoch, receipt; gluck socket, spain tailnet, spain asks gluck's hatch | tampered argv, replay, expired, wrong host, wrong base, unlisted argv[0], closed hatch, gluck unreachable: refused; one run end to end in a VM test |
| K3e | batches, and `role.delegate` template writing a standing class grant | partial batch stops, batch replay, item swapped; grant with past `until`, values beyond class |

Done when, as the lane says: Gluck approves a `root.run` on spain with his passkey (K3d), and
`sudo figaro hatch close` revokes all King delegation (K3a).

## 5. Gluck's questions 4 to 6

**Q4. May the King set provider and model without a click?**
Not by default. Provider and model are spend and data routing. Recommendation: no standing
power; each change by passkey, or one passkey-approved standing grant that names the providers,
the models and an expiry of at most 30 days. ttl, lifetimes and fan-out inside quota: yes,
without a click. This changes `lane/king-delegate`, which today delegates provider and model with
no click and no expiry.

**Q5. Escape hatch as owner terminal plus a kill switch the King cannot write, tested before
delegation goes live?**
Yes. K2b makes it structural: delegation reads the hatch and is off when the file is missing, so
it cannot go live until K3a writes the file, and K3a ships with the sabotage test and the drill.
Run the drill once before the first `hatch open`.

**Q6. Passkey in Authelia step-up or in the approvals app?**
In the approvals app, verified by a root executor; Authelia stays the door. Trade-off: a second
enrolment and a WebAuthn package, against a signature that names the exact digest and does not
fall if spain root or Authelia does (section 1). If Gluck prefers one place, the Authelia
variant works with the same plan and executor; the executor then checks the ID token's
`nonce`, `amr` and `auth_time` against Authelia's pinned key instead of the assertion.

## Open

- Inspector draft 2, item 2: forks copy `elicit.*`. It matters once figaros file on their own
  forms; `root.run` files on role forms only until then.
- The command allowlist per host: El Zapador and El Mayor name the first entries
  (`nixos-rebuild switch --flake <keel rev>#spain`, `systemctl restart <unit>`).
- Merlin: the hatch node moves with the cutover; spain then asks Merlin.
