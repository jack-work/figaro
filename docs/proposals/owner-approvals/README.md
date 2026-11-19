# Owner approvals and the troupe gateway

Status: proposal, draft 2 (2026-11-19). Plan only; nothing is built until Gluck approves.
Base: keel/main v0.77.1 `ca2680c3b`. Brief: `~/notes/spain/briefs/owner-approvals-gateway.md`,
including Gluck's revision of 11-19 ~15:10 ("not figaro-native"). Threat rows from El Llavero:
`~/notes/spain/program/owner-approvals/llavero-threat-input.md`, cited as L1 to L10.

## Intro

Some keys only the owner writes: `system.provider`, `system.model`, `system.grants.outfits` and
the rest of `WriterOwner` (`api/form/registry.go:44-81`). The daemon refuses them to every
figaro's tools (`internal/angelus/box_keys.go:300-309`, messages `:338-345`), and hands managed
apply's operator prefixes only to an owner call that is not a figaro and not a box
(`internal/angelus/form_apply.go:152-156`). That stays. The cost is that the King, steered over
Telegram, waits for Gluck to reach a terminal.

The design, per Gluck's revision:

1. **File.** A figaro writes a request into its own form, under `requests.<id>`: verb, target,
   proposed values, reason, expiry. Nothing new in the daemon; a figaro already writes its own
   non-system keys.
2. **Show.** A thin web app, `figaro-approvals`, behind Authelia (admin group, two factors),
   reads those forms with figaro-forms 0.1.x and lists the requests.
3. **Approve.** Each button maps to one patch built by the app's backend from a fixed per-verb
   template and validated values. Approve removes the request and sets the owner-held key;
   Deny removes the request. The request text is never applied.
4. **Act as the owner.** The backend is a user service on gluck, outside any figaro's cgroup,
   so the daemon judges it the owner. No new principal, no new bypass, no new daemon code in v1.

There is no broker with a queue of its own. The queue is the forms; the "broker" shrinks to the
backend's two POST handlers.

## Request shape (what a figaro writes)

```json
"requests": {
  "r7f3a": {
    "verb": "set.model",
    "target": "d361163c0094c2ae4ad60aaad48ad258",
    "values": {"provider": "anthropic", "model": "claude-opus-4-5"},
    "reason": "King wants opus for the release audit",
    "expires": "2026-11-19T18:02:11Z"
  }
}
```

Everything here is untrusted (brief revision point 1). The app treats it as a suggestion of
which template to offer and which values to try, nothing more:

- `verb` must name a template in the allowlist below, or the row shows as "unknown verb" with
  Deny only.
- `target` must be an existing aria id or `@role` and is re-read from the daemon, with its
  name and current values, before the page shows the row.
- `values` are validated per template (enums, existence, bounds). A value that fails makes the
  row Deny-only and says which field failed.
- `reason` is shown escaped and never touches a patch.
- `expires` past means Deny-only. Capped at 24h after the form version that wrote it.
- The filer is not a field: it is the form the request sits in. A figaro can write only into
  its own form (WriterSelf), so a request cannot claim to come from someone else.

The id is the filer's choice, `[a-z0-9]{1,16}`; the app refuses any other id shape.

## The patch

The daemon already has what makes one click one write. `figaro.set` takes `IfVersion`, which
refuses unless the form is still at that version, and `Assert`, which makes removing an absent
key a refusal rather than a no-op (`api/rpc/turn.go:93-103`, applied at
`internal/angelus/hubs.go:160-165`).

**Approve, target is the filer** (a figaro asking for its own model, say): one `figaro.set` on
the filer's form, `Assert: true`, `IfVersion` = the version the page rendered:

```
remove requests.r7f3a
set    system.model = "claude-opus-4-5"
```

Atomic. A stale click, a second click, or a request edited after Gluck looked at it all fail
on the version or the assert, and nothing is written.

**Approve, target is another form** (the King asking for a seat's model, the common case): a
daemon patch is atomic on one form only. Two writes, consume first:

1. On the filer's form: `remove requests.r7f3a`, `Assert`, `IfVersion` = rendered version. This
   is the claim; whoever wins it is the only one who may apply.
2. On the target's form: set the key, `IfVersion` = the target version the page rendered (the
   page showed its current value at that version).

If step 2 fails, the request is gone and the key unchanged: it fails closed, the audit line says
`claimed, not applied`, and the King files again. That is the one place this shape is weaker
than a single patch. A multi-form transaction in the daemon would close it; I do not think it is
worth building for this (open question 5).

**Deny:** `remove requests.r7f3a` with `Assert`, optionally `set requests_denied.r7f3a = "<reason
Gluck typed>"` in the same patch, so the filer learns why. The filer removes that key when read.

**Templates.** Each verb is a function in the backend from (validated target, validated values)
to a patch. The request's JSON is never spread, merged, or passed through. Only the keys in the
template's own list can appear in the patch, and the backend checks the built patch's touched
keys against that list before sending, using figaro-forms' `touchedKeys`
(`packages/figaro-forms/src/patch.ts:368`).

## Verb allowlist (v1)

| verb | values | validation | patch on target |
|---|---|---|---|
| `set.provider` | `provider` | a configured provider name | `system.provider` (`registry.go:46`) |
| `set.model` | `provider`, `model` | model in that provider's list | `system.provider`, `system.model` (`registry.go:46,51`) |
| `grants.outfits.add` | `outfit` | outfit exists by name; no globs | append to `system.grants.outfits` (`api/form/outfit_grants.go:11`, `registry.go:81`) |
| `grants.outfits.remove` | `outfit` | present in the list | remove from it |
| `fanout.quota` | `quota` | integer 0..32 | `system.fanout.quota` (`api/form/protected.go:19`, `registry.go:84`) |
| `succession.release` | none | key is set | unset `system.succession_failed` (`registry.go:77`) |
| `role.apply` | `role`, `manifest` | manifest by name from the root-owned Nix manifest dir, with its hash | `form.apply` with `IfVersion` (`api/rpc/form_apply.go:7-14`, operator prefixes `internal/formapply/managed.go:12`) |

`role.apply` is not a `figaro.set`: it is `form.apply`, which already requires `IfVersion` and
an expected outfit (`internal/angelus/form_apply.go:29`). The claim on the filer's form comes
first, as above.

Out of v1:

- **Key grants** (`write:<target>#<key>` scopes). They widen what one figaro may write on
  another, which is the fence's whole job, and Llavero asks them out (L7). The key-grant fence
  (`box_keys.go:115`, `:424-440`) is untouched.
- **Anything with a path, env, or shell.** Valet grants (`internal/sandbox/valet/grants.go:24-30`)
  stay with Gluck's own shell.
- **Fermata hold.** No key for a hold exists in v0.77.1; I read it as releasing a stalled
  succession (`succession.release`). Open question 4.

## The app

```
phone ─ Caddy on spain (order.kelliher.info/approve/*) ─ Authelia: admin, two_factor
          └─ tailnet ─ gluck tailscale0:7482 ─ figaro-approvals (user service, as gluck)
                                                  └─ unix ─ angelus (judged owner)
```

- **Reads** use figaro-forms 0.1.4 (`eb07dd7`): a server-side `FormsClient` follows every live
  figaro's `requests` key and each target's affected keys; the page renders rows with
  `figaro-forms/view` (`flatten`, `resolveAction`, `packages/figaro-forms/src/view/index.ts:169,246`)
  and Zanni's styling. The browser gets read-only data and never holds a door connection that
  can write.
- **Writes** are two POST endpoints on the backend, `/approve/<filer>/<id>` and
  `/deny/<filer>/<id>`, carrying the two versions the page rendered. The backend rebuilds the
  patch from the template, never from the browser's body beyond ids, versions and an optional
  deny reason.
- **Not the forms bridge with `--write-system`.** That flag lets a page write any `system.*`
  key (figaro-forms `README.md:56-90`), so the page's JavaScript would be the only gate. The
  backend holds the write path and exposes just the two verbs.
- **The gluck bridge.** Reuse its tailnet path and Caddy site on `order.kelliher.info`, but a
  separate route, port and upstream, not the `--hierarchy-only` bearer on 7481 (L9). ufw on
  gluck admits 7482 from spain's tailnet address only; Gluck runs that, with the 7481 rule.
- **Unit.** `systemd --user` service, `NoNewPrivileges`, `PrivateTmp`, `ProtectSystem=strict`,
  read access only to its own dir and the manifest dir, no read of `~/.config/figaro` secrets or
  hush state. Its cgroup is its own, so once the peer caller check lands
  (`proposal/laws` `cd64246e8`, `docs/proposals/laws/README.md:66-96`) the daemon tells it from
  a figaro's tool by cgroup, not by environment.
- **Identity front.** Authelia now, because it is what runs on `*.kelliher.info`; Kanidm when it
  replaces Authelia as the one login Gluck named on 11-19. The backend verifies the edge's claim
  itself (below), so the swap is config. Open question 1.

## Audit

One JSON line per click to the user journal with a fixed `SYSLOG_IDENTIFIER=figaro-approvals`,
and a copy to `~/.local/state/figaro-approvals/audit.log`:

`time, action (approve|deny), filer, request id, verb, target, the patch's keys and values,
filer form version, target form version, result (applied | refused: <reason> | claimed, not
applied), approver sub, auth method, front (page|telegram)`.

v1 verbs carry no secret values (providers, model names, outfit names, integers, manifest
names), so values are logged; a template that ever carries a secret marks the field and the line
records only its hash. `reason` is not logged, since a figaro wrote it. Spain's Authelia log is
the second, independent record of who logged in.

The daemon's own form history also records each write with its version, so the form shows the
change even if the app's log is lost.

## Who files, who sees

- **Files:** any figaro, only into its own form. Nothing else to enforce: the daemon already
  refuses a figaro writing another's non-system keys without a grant.
- **Sees:** only Gluck, through Authelia admin. Figaros read their own request and learn the
  result by the request vanishing and the key changing (or `requests_denied.<id>` appearing).
- **Noise:** the app shows at most 20 pending per filer and flags a filer with more.

## Telegram

The herald already sends inline keyboards and routes callbacks by prefix (`gluck-herald`
`3ebafab`, `internal/telegram/api.go:169-175`, `internal/route/callbacks.go:10-35`).

- When the app sees a new request it asks herald to message Gluck: verb, target name, new
  value, and a URL button to `/approve/<filer>/<id>`. The button opens the page; the page's
  button sends the same template patch. One mechanism, two doors.
- Not v1: a callback button that approves in the chat. If Gluck wants it, the callback carries
  filer, id and both versions, herald checks Gluck's sender id and posts to a backend endpoint
  that admits only herald's uid and only `succession.release` and `fanout.quota` lowered.
  Telegram has no second factor (L8). Open question 3.

## Spain and Merlin later

Recommendation: **one app per node**, each on its own node as that node's owner, behind the
same Authelia, at `order.kelliher.info/approve/<node>/`. An owner patch must come from that
node's owner; a federated writer would be a cross-node owner, which is the bypass we are not
building. The landing page can list nodes and their pending counts by reading each app's
read-only list. Open question 6.

## Threat model

Llavero's rows, answered against this shape:

| # | threat | answer |
|---|---|---|
| L1 | Same uid: a figaro drives or rewrites the approver | There is no queue or DB to rewrite: the state is forms, which the daemon guards. The backend runs as gluck, as figaros do in home mode, so a tool could talk to the daemon as owner, but it can already (issue #57); the app adds no hole and closes none. The peer caller check makes the backend's cgroup, not its uid, the owner signal; house mode (`proposal/deployment-modes` `480ce9e4a`, `README.md:98-133`, `:336-381`) makes it real. Not a new bypass: the backend is exactly the owner the daemon already recognizes (`internal/angelus/sandbox_grant.go:163-171`, `internal/authz/door.go:78-80`, `internal/authz/authz.go:109-112`). |
| L2 | The edge vouches for Gluck | v1 trusts Authelia's forward-auth only at the edge, and the backend verifies an Authelia OIDC ID token on each POST: `aud` = figaro-approvals, `amr` with a second factor, `sub` = the admin user, `auth_time` under 5 minutes, `nonce` = SHA-256 of (filer, id, both versions). Spain root still owns Authelia and can mint that token. The fix is a WebAuthn assertion over the same nonce, verified against a key pinned in Nix on gluck: rollout step 6. |
| L3 | Shown is not run | The page shows the patch the template builds, keys and values escaped and whole, next to the current values. The POST sends versions, and the backend rebuilds the same patch at those versions; any change in between fails `IfVersion`. Values with control, bidi or confusable characters fail validation. |
| L4 | Replay and staleness | The approve patch removes the request with `Assert`: a second click finds nothing and writes nothing. `IfVersion` on both forms voids an approval when either form changed since render. Expiry is checked at click. |
| L5 | CSRF | POST only, `Origin` check, `SameSite=Strict` cookie, and the OIDC step-up nonce bound to the request. |
| L6 | Filer approves | No figaro credential reaches the POST: it needs an Authelia admin session with two factors and a fresh ID token. The order bridge bearer, herald's identity and hush entries yield none of these. Filer is the form, shown on the page. |
| L7 | General sudo | Seven templates; no paths, env, or free text in a patch; touched keys checked against the template's list; no exec; hardened unit; key grants out of v1. |
| L8 | Telegram as a factor | v1: Telegram only opens the page. Later, low-risk verbs only, sender id checked. |
| L9 | Gateway sprawl | Own route, port and upstream; not the 7481 bearer; per-path admin and two-factor rules; no fallthrough, as in umbral. |
| L10 | Audit | Journal plus file per click, fields above, no tokens, no figaro-written text; Authelia's log on spain is independent; the daemon's form history records every write. A tool running as gluck can rewrite the file but not the system journal's sealed entries or spain's log. |

Rows the brief adds:

- **Forged request.** Filing is not power: a request is a row on a page. The filer is the form
  it sits in.
- **Compromised figaro or the King.** It can file noise and write a misleading `reason`. The
  page leads with the patch, not the reason, and caps rows per filer. It cannot approve.
- **Stolen phone session.** Telegram alone approves nothing. An open Authelia session still
  needs a step-up within 5 minutes. A phone that is unlocked, logged in and holds the second
  factor is Gluck; revoking in Authelia stops it.
- **The page itself.** Static files from the Nix store, strict CSP, no inline script, no
  third-party code, no write-capable door in the browser. A defaced page can show a wrong row,
  but the backend builds the patch, and the nonce binds the token to the ids and versions.

## How it fits deployment modes and laws

- **Home mode, today:** the backend is the owner because it is gluck outside a figaro; honour
  until the peer caller check lands, the same as every owner call.
- **House mode:** the daemon runs as `figaro` and the owner is named in config
  (`deployment-modes README.md:98-133`). The backend runs as the person, outside the valet's
  cgroup, so it is the owner by that binding. Its unit and config come from Nix, root-owned,
  so "nothing a figaro writes configures its own fence" (`:336-345`) holds.
- **Laws:** `fanout.quota` is the owner raising a budget the lineage cannot give (laws part 2,
  `README.md:307-370`). Laws and quotas still bind the figaro the approval touched.
- **Authz middleware** (`proposal/authz-middleware` `d4576a91d`): a refusal's `Decision.Fix`
  (`README.md:59,99`) can name the exact `figaro set requests.<id> ...` line for an
  allowlisted verb, so the King files from the refusal it was given.

## Rollout

Each step ships alone.

1. **Templates and validation** as a library with golden tests: request in, patch or refusal
   out. No service.
2. **Read-only page.** The app on gluck, Caddy route, ufw 7482, Authelia rule, list of
   requests with the patch each would apply. Zanni styling. Nothing writes.
3. **Deny.** One POST, one patch. Exercises `Assert`, `IfVersion`, audit, OIDC step-up.
4. **Approve `set.model`.** Same-form and cross-form paths, end to end.
5. **The other verbs**, one per commit; herald notices with the URL button.
6. **WebAuthn** pinned on gluck, required for `role.apply` and `grants.outfits.*`; Kanidm when
   it is live.
7. **Optional:** Telegram callback approve for low-risk verbs; a node list for Spain and Merlin.

No daemon change is needed for steps 1 to 5.

## Open questions for Gluck

1. **Identity front:** Authelia now and Kanidm later, verified as OIDC by the backend? Or wait
   for Kanidm?
2. **WebAuthn:** worth step 6, given it is the only answer to spain root owning the login?
3. **Telegram:** open the page only, or approve low-risk verbs in the chat?
4. **Fermata hold:** releasing a stalled succession, or a hold you want to place (no key today)?
5. **Cross-form approve:** accept claim-then-apply (fails closed), or have the daemon take a
   two-form transaction?
6. **Spain and Merlin:** one app per node (recommended) or a federated list?
7. **Key grants:** out of v1, or in behind WebAuthn?
