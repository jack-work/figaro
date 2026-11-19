# Owner approvals

Status: v1 slice built on `proposal/owner-approvals`, not started. The Inspector reads it
before it runs. Base: keel/main v0.77.1 `ca2680c3b`. Brief:
`~/notes/spain/briefs/owner-approvals-gateway.md` with Gluck's revision and decisions at the end.
Threat rows: El Llavero, `~/notes/spain/program/owner-approvals/llavero-threat-input.md` (L1 to
L10) and his review of draft 2, `llavero-review-draft2.md` (B1 to B4, N1 to N5).

## What it is

Some keys only the owner writes: `system.provider`, `system.model`, `system.ttl`,
`system.grants.outfits` and the rest of `WriterOwner` (`api/form/registry.go:44-105`). The
daemon refuses them to every figaro's tools (`internal/angelus/box_keys.go:300-309`, messages
`:338-345`). That stays. The cost was that the King, steered over Telegram, waited for Gluck to
reach a terminal.

Now:

1. **File.** The King writes a request on its role form `@96447061` under `elicit.<id>`. Seats
   may file on their own role forms. Requests survive succession because the role form does.
2. **Show.** `figaro approvals serve`, a small HTTP service run as Gluck in his
   `systemd --user` manager, lists every `elicit.*` on the forms it is given. For each request
   it builds the patch from a fixed per-verb template and the live current values, and shows
   the exact patch, current beside new, with its sha256. The request itself is shown only as
   escaped text under "Filer's words, unverified".
3. **Approve** posts the hash. The server rebuilds the patch from fresh reads, refuses unless
   the hash matches, then writes with compare-and-set on the versions it read. **Deny** removes
   the request with compare-and-set.
4. **As the owner.** The service runs outside any figaro, so the daemon judges its calls the
   owner's. No new principal, no daemon change.

Code: `internal/approvals` (plan, templates, decide, server, page), `internal/cli/approvals.go`
(the verb and the daemon adapter), `contrib/systemd/figaro-approvals.service`.

## Request shape

```json
"elicit": {
  "r7f3a": {
    "verb": "figaro.model",
    "target": "0123456789abcdef0123456789abcdef",
    "set": {"system.provider": "anthropic", "system.model": "claude-opus-4-5"},
    "reason": "release audit wants opus",
    "expires": "2026-11-19T18:02:11Z",
    "state": "pending"
  }
}
```

All of it is untrusted. Any unknown field, unknown verb, wrong target shape, extra or missing
`set` key, bad value, or an `expires` that is past or more than 24h out makes the row Deny-only,
and the page says why (`internal/approvals/plan.go`, `Build`). `reason` and `state` never reach
the plan. The id is `[a-z0-9]{1,16}` and not `denied`.

## Verbs (v1: two per level)

| verb | target | keys | values |
|---|---|---|---|
| `figaro.model` | a figaro, full 32-hex id | `system.provider`, `system.model` | provider `[a-z][a-z0-9-]{0,31}`, model `[A-Za-z0-9][A-Za-z0-9._:/-]{0,127}` |
| `figaro.ttl` | a figaro, full 32-hex id | `system.ttl` | `0` or `<n>[smhdw]` |
| `role.model` | a role form `@hex` | `system.provider`, `system.model` | as above; the page says "takes effect at the next succession" |
| `role.outfits` | a role form `@hex` | `system.grants.outfits` | 1 to 16 existing outfit names, no globs; the whole new list |

Per-figaro verbs write the target's own form. Per-role verbs write the role form, which dresses
the next holder at succeed and seat. No `@role` is ever resolved to its holder: the adapter
refuses when the daemon attaches an id to a different form (`internal/cli/approvals.go`,
`daemonForms.aria`). That answers B3: a recast between render and click cannot move the write.

Every value is ASCII and passes its pattern, so no control, bidi or confusable character reaches
a patch. Outfit names are checked against the daemon, and each outfit's folded form is hashed
into the plan (`inputs`), so an outfit edited after render changes the hash (B2, partly B1).

Not in v1: `role.apply`, key grants, fan-out, anything with a path, env or shell (L7, B4).

## The patch and the hash

The plan is `{filing, filing_version, id, verb, target, target_version, lines[{key, current,
new}], inputs, applied}`. Its sha256 over that JSON is the hash shown and posted. It covers the
current values Gluck saw, the new values, both form versions and the outfit digests. The
request's text is not in it; only the template's validated output is.

The approve writes:

- **Target is the filing form** (`role.*` on the King's own role form): one `figaro.set` with
  the keys and the removal of `elicit.<id>`, `IfVersion` = the version read. Atomic.
- **Target is another form** (the usual case): two writes, in Gluck's accepted order:
  1. the target's keys, `IfVersion` = the target version read;
  2. the removal of `elicit.<id>` on the filing form, `IfVersion` = the filing version read.

  **This is not one atomic patch.** A daemon write touches one form
  (`internal/store/xwal_backend.go:629`). If the service dies between the two, the key is set
  and the request still pending. The page then shows "already applied", and approving it again
  only removes the request (tested: `TestCrashBetweenWritesThenReapproveOnlyRemoves`). Gluck and
  the King accepted this; a two-form transaction in the daemon would remove it.
- **Target already holds the values:** only the removal.

Stale reads: the hash covers both versions, so any write to either form after the page read it
changes the hash and the approve is refused before it writes. A write landing between the
server's re-read and its set fails the daemon's `IfVersion`. A second click finds no request.

`figaro.set` carries `Assert`, so removing an absent key is a refusal (`api/rpc/turn.go:93-103`,
`internal/angelus/hubs.go:160-165`).

## Audit

One JSON line per decision, appended to `~/.local/state/figaro-approvals/audit.log` (0600) and
written to stdout for the journal: time, action, approver, filing form, request id, verb,
target, hash, result. No values from the keys, no request text, no tokens.

## The web side

- `GET /` lists, `GET /r/<form>/<id>` shows one, `POST /approve` and `POST /deny` decide.
  Plain HTML from `html/template` (auto-escaped), one CSS file, no script. CSP
  `default-src 'none'; style-src 'self'; form-action 'self'; frame-ancestors 'none'`.
- Every request needs `Remote-User` equal to `--admin`; every POST needs `Origin` equal to
  `--origin`, and a token, an HMAC with a per-process key over form, id, hash and version. A
  post whose hash was not the one rendered fails the token.
- Styling is plain, after Il Zanni's notes (`~/notes/zanni/owner-approvals-styling.md`):
  sentence rows, the exact patch as the centre, current struck beside new, a sticky
  Approve/Deny bar, results in words. The zanni components come later.

Route (El Mayor): the existing order site on spain gets a second handle,
`handle_path /approve/*`, upstream `gluck.tail34d120.ts.net:7482` by tailnet name, no
fallthrough, and an Authelia rule for `/approve/*` (admin user, two_factor) placed before any
broader `order.kelliher.info` rule. Not the 7481 bridge bearer (L9). ufw on gluck: 7482 from
spain only, Gluck's step; after the Merlin cutover it moves into the charter's tailnet firewall
aspect. The unit binds `100.92.208.109:7482`.

## Threat model

Home mode is not a boundary, and this slice does not pretend otherwise. Figaros run as gluck.
Any figaro whose tools reach a shell can already call the daemon as the owner (figaro issue #57),
and can also reach `100.92.208.109:7482` locally and send `Remote-User: gluck`. The page adds
ergonomics, not a wall. The guarantees below hold against the network and against request
content; against a local figaro they wait for the peer caller check and house mode.

| # | threat | answer in v1 |
|---|---|---|
| L1 | Same uid | Admitted, as above. A figaro could also edit the unit through a drop-in (N1). The service holds no state of its own beyond the audit file; the queue is the forms the daemon guards. House mode (Merlin): run it as the person outside the valet, the valet hiding its state dir. |
| L2 | The edge vouches for Gluck | Yes in v1: the server trusts Authelia's `Remote-User`. The OIDC step-up Llavero specified (PKCE, `max_age`, `nonce` = plan hash, `amr`, `sub`) is the next step, and then WebAuthn. Spain root owns Authelia either way. |
| L3 | Shown is not run | One plan; the page renders it; its hash is posted; the server rebuilds it and refuses on a difference; templates write only their own keys. |
| L4 | Replay, staleness | `IfVersion` on every write, versions in the hash, `Assert` on removal; a second click is a refusal. |
| L5 | CSRF | POST only, Origin check, HMAC token over id, hash and version, CSP `form-action 'self'`. |
| L6 | Filer approves | Over the network: no figaro credential yields an Authelia admin session. Locally: see the home-mode paragraph. |
| L7 | General sudo | Four templates, patterned values, no process started: a test fails if `internal/approvals` imports `os/exec` or `syscall`. |
| L8 | Telegram | Not wired. When it is, the button is a URL to `/r/<form>/<id>`. |
| L9 | Gateway sprawl | Own route, port and upstream; not the 7481 bearer. |
| L10 | Audit | Append-only by flag, not by the kernel; a gluck process can rewrite it. The journal copy is root-owned (no Forward Secure Sealing, N2). |
| B1 | Validation lookups are figaro-writable | Outfit names are checked and their folded content hashed into the plan, so what Gluck approves is pinned; but the outfit files are still gluck's to write. Providers and models are checked by pattern only. Root-owned grantable lists are open. |
| Estate | Spain has no person-owner (El Mayor) | Out of v1; one app per node later, and Spain's needs a service principal and its own row here. |

## Tests

`go test ./internal/approvals`: hash mismatch refused; an edit after render changes the hash;
stale CAS refused (the target moves between read and write); stale deny refused; unknown verb
refused; bad values (shell text, extra key, `@role` on a figaro verb, bidi, glob, missing
outfit, bad ttl, unknown field, expiry) refused; request prose never reaches the plan and the
package starts no process; the crash between the two writes, then re-approve only removes;
same-form approve is one write; non-admin refused; request text escaped; cross-origin and
forged-hash posts refused; approve round trip; second click refused. Canaries: dropping the
hash compare fails the hash tests; dropping the fake's version check fails the stale test.

Read-only smoke against the live daemon: the server listed `@96447061` ("Nothing waiting.") and
refused a request without `Remote-User`.

## Running it (Gluck, after the Inspector)

The unit calls `~/.nix-profile/bin/figaro`, so it needs a figaro release carrying this branch.

```
mkdir -p ~/.local/state/figaro-approvals
cp contrib/systemd/figaro-approvals.service ~/.config/systemd/user/
systemctl --user daemon-reload && systemctl --user start figaro-approvals
sudo ufw allow in on tailscale0 from <spain tailnet ip> to any port 7482 proto tcp
```

Then El Mayor's CAND on spain (route and Authelia rule).

## Open

1. OIDC step-up (Llavero owns the client) before anything wider than these four verbs.
2. WebAuthn pinned on gluck.
3. A two-form transaction in the daemon, to make cross-form approve atomic.
4. Root-owned lists of grantable outfits and selectable providers (B1).
5. Telegram URL button through herald.
6. Spain and Merlin: one app per node; Spain's owner is a service principal.
7. Forks copy `elicit.*` from an aria's form (Inspector). Role forms are not forked, so v1
   lists only the role forms it is given; per-figaro filing would need the key dropped at birth.
