# Kingdom, Track E: the estate (north-star plan)

Owner: Figaro Strategist (Estate), role `@61e9ab47ab87d6ec2eab7703d1deffe7`.
Vanguard: Figaro Vanguard (Estate), role `@6a8e424e4a0808e542186851c23a3abf`.
Parent plan: `~/.local/state/impresario/plans/kingdom-charter.md` (the Figaro Engineer, `@ee927149`).
Design: [deployment modes, revision 2](https://keel.kelliher.info/_keel/jack-figaro/blob/480ce9e4a323116acb2f856550286cf89311de4a/docs/proposals/deployment-modes/README.md?view=preview) (`proposal/deployment-modes` `480ce9e4a`).
Binding feedback: `~/.captains-log/2026/11/18/king-feedback-kingdom-charter.md` (King of Spain, 2026-11-18).

## Goal

A kingdom charter in Nix that defines every role, outfit, permission and
fan-out budget, deployed in the right mode per host: Merlin in house mode,
spain in estate mode, cheroot in home mode, the dad case in remote mode; hush
a system service; the valet the only way tools touch a person's files; hosts
federated through the troupe.

## Rules of the road

- Nobody switches Merlin or spain. Merlin work waits on Gluck's cutover and
  goes through El Mayor (`@2b4fe37f`, holder of cheroot-config) and Merlin's
  figaro (echoless-iron `4bdf2e4b`). Spain windows are daytime, King-named,
  El Inspector reviews, El Zapador executes.
- cheroot-config changes stage on branches for El Mayor; figaro and hush
  changes stage on `lane/<change>` for the Engineer. Nothing merges here.
- Tests: NixOS VM tests (`nixosTest`) for every unit, socket and user; nix
  devshells and an isolated tmux socket for CLI behaviour; store snapshots
  only. Startup time and first-output time are measured before and after any
  change to the angelus's start path.
- Budget (the King): vanguard and its reviewer on Sonnet, except reviews of
  hush, authz or store changes (Opus). Few, large turns.

## Order

| # | change | repo, branch | acceptance | breaks | reviewer |
|---|---|---|---|---|---|
| E0 | **Kingdom fan-out.** Roster role gains `maxChildren`; the reconcile hook applies it with `figaro form apply --id <form> --managed system.fanout.max_children` (needs figaro with `lane/managed-fanout` 5f83ebbac). Values per seat in `desktop-system/modules/kingdom.nix` from the King's census (ask him once if not in captains-log) | cheroot-config, branch off `proposal/system-charter-split` (5d2273f) | flake check green; an eval check asserts the rendered roster TOML carries `max_children`; the reconcile dry run against a store snapshot with that figaro shows one planned change per seat and nothing else | none until applied; then seat forms gain the key | Sonnet |
| E1 | **Angelus socket activation** (stage 2). Named listeners from `LISTEN_FDS`/`LISTEN_FDNAMES`; `figaro.socket` in `nix/module.nix`; the daemon still binds when not activated | figaro, `lane/socket-activation` | VM test: CLI succeeds with the service stopped, socket up; door sockets keep their admission; startup time measured, no regression | none; `socketActivation = false` reverses | Sonnet |
| E2 | **hush as a system service** (stage 1). `services.hush`: user `hush` (uid 476 kept), `credUsers`, drop box socket, admin socket; `services.hush-cred` an alias for one release | hush, `lane/hush-system-service` | `cred-vm` extended: figaro uid gets a bearer, another uid refused, drop box write-only; existing consumer checks green | spain path rename at its window (folded into El Mayor's charter-consumer branch, daytime, login state moved not copied) | Opus |
| E3 | **`figaro valet` and the fence** (stage 3). Subcommand; per-call socket activation in the user's manager (`Accept=yes`); allow-all grant table first, timed grants second; the Go fence (userns, mount namespace hiding `/run/user`, nix daemon socket, cheroot-config and the grant table; Landlock second); refuses to start without unprivileged userns; `figaro doctor mode` | figaro, `lane/valet` | VM test: a tool sees only granted paths, cannot connect to the daemon socket or hush, runs in its own cgroup; kill switch stops it; memory per call measured (about 13 MB expected) | none; opt-in per outfit | Opus |
| E4 | **House mode module** (stage 4, staged only). `services.figaro.mode = "house" \| "estate"`, `owner`, `users`, `valet.users`, `localTools = false`; one-shot store import to `/var/lib/figaro`; charter `my.figaro.mode` gains `house`/`estate` (`system` aliased) | figaro `nix/module.nix`, `lane/house-mode`; cheroot-config branch for El Mayor | VM test of a house-mode host with one person: fork, tool via valet, local refused, owner binding present; import test on a snapshot | the store moves; CLI stops spawning (named in the module docs) | Opus |
| E5 | Spain U1 / estate mode proper | spain-flake via El Mayor | daytime window | spain figaros on `local` refused | Opus |
| E6 | hush federation, key ceremony | hush, spain-flake | mTLS + WhoIs pin VM test | node hush state deleted | Opus |
| E7 | Remote mode (the dad case) | figaro node door, spain | a remote CLI runs a figaro whose tools are in a spain box | none locally | Opus |
| E8 | Kingdom module, whole: modes, outfits per kind, permissions, regiment kinds, federation fields | cheroot-config | needs Track P's P4, P5, P6 | role forms carry rendered scopes and budgets | Opus |

E0 to E3 are independent; the vanguard takes them in that order (E0 is the
King's first Italy item). E4 starts when E1 to E3 are staged and waits on
Gluck's cutover to deploy. E5 to E8 are after this night.

## Open for Gluck (via the Engineer)

Questions 3 to 5 of the deployment-modes revision: rename `my.figaro.mode`
`system` to `house`; regiment kinds as new roster kinds; remote mode after
house mode.

## Obligations before a host leaves hush-cred (from E2's runbook)

1. rou2 needs a control socket against the new layout (status and refresh moved to the admin socket) before any rou2 host switches from `services.hush-cred` to `services.hush`. Owner: rou2's Engineer, via the King.
2. hush `feat/anthropic-max-login` (worktree /var/tmp/hush-anthropic-max) must merge before the move, so the anthropic-max sign-in state moves under the new service. Owner: the Figaro Engineer (hush).
Both gate spain's daytime window, not tonight's work.
