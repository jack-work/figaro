# Kingdom: status board

One Strategist plans both tracks and fans out forks that build parts. The Knight carries non-conflicting changes, produces the validated build and asks the Review Steward for a reviewer. Only the Engineer merges.

- Plans: [permissions.md](permissions.md) (Track P) and [estate.md](estate.md) (Track E). Deployment modes, revision 2: keel `proposal/deployment-modes` 480ce9e4a.
- Seats (roles; their names live in the charter, never in figaro Go): Figaro Strategist `@9bf485c2`, Figaro Knight `@d121c404`, Figaro Review Steward `@9e5bdab3`. Reviewers are turn-1 forks of the Strategist on Gemini, one per change.
- Validated builds: `/var/tmp/kingdom-builds/<part>`.

## The whole plan (14 items), board order

Working agreement: REVISION 2 of the plan root. The Strategist builds everything at once with its forks, keeps the final vision, and restacks it into non-conflicting commits for the Knight as main moves. Reviewers fork the Strategist at turn 1 (the common fork point) on Gemini.

**PAUSED 11-18 06:xx by King order (quota 52%) until Gluck answers.** On resume, first: one Opus review of rel/v0.74.5 657c554ce (P3+H1, gate green, parked); verdicts from the P2+P5 and E1 delta reviews; restack P4 (scratch dac29e5ff) on v0.74.5; E3 resumes from scratch/valet 238a6fcf8, after folding E1's two LOWs. No new lanes or reviews; in-flight reviews (P2+P5, E1 delta) and gates (v0.74.5) finish. Fan-out forks harvest and retire; branches stay.

| # | part | builder | branch | state (2026-11-18 02:xx) |
|---|---|---|---|---|
| 1 | IF1 study_incantation string | Knight | `lane/study-incantation-string` | in v0.74.3 (cutting) |
| 2 | P1 key registry | Knight | `lane/key-registry` 63de1c6f4 | SHIP (coverless-lullaby); P2/P5 restack on it |
| 2b | H1 place --under; succeed --successor drawn in place | fork oceanic-degree `a78f3129` | `lane/hierarchy-place` | `lane/hierarchy-place` a40686c8d on P3 | READY; ships with P3 as one stack in v0.74.5, one batched review after v0.74.4 |
| 2d | F2 owner check ignores the figaro claim on the local door (pre-existing; cap bypass, role hijack) | Knight | off 0911f728f | review FIX (aardvark-amazingly): scoped demote/succeed regression being fixed; v0.74.4 = F2 + that fix + callerOwns local door + dead-claim-as-owner + consent leak; delta review next. Unscoped topology reach moves to P5 |
| 2c | P1b shape checks, figaro doctor keys | Knight | `lane/key-shapes` bf12f271f on 63de1c6f4 | SHIP (academy-kite, all A); lands with P2+P5 so P5 calls the shapes; three nits after the pause |
| 3 | E0 kingdom fan-out (charter `maxChildren`) | fork tavern-culprit `df4d5475` (drawn under the Engineer) | cheroot-config, off `proposal/system-charter-split` | finishing |
| 3b | R1 kingdom regiment | Knight | cheroot-config `lane/kingdom-regiment` e86f3dc (on 579193c) | SHIP (explode-nugget, no findings); owner lands eced4cd, ac170c8, e86f3dc, then flake.lock with f11866f + 579193c |
| 4 | P4 peer caller check | fork anagram-erratic `0e15cab0` | `lane/peer-caller-check` de5d64574 | fixes in (finite ttl from tools, succeed carries the seat ttl, peer-check.md); waits on v0.74.4 to restack, then review |
| 5 | E2 hush as a system service | fork erasable-unzip `56faa8d6`; drop-box refresh by the Strategist | hush `lane/hush-system-service` 70d3782 + `lane/hush-drop-refresh` | SHIP; landing branch hush lane/hush-landing e2497c5be (gate green) with the Engineer to merge |
| 6 | P3 admission ledger | fork dominoes-elusive `439448d3` | `lane/admission-ledger` a7ec40e88 | restacked on 0911f728f with the shared seat op (figaro.seat); restacks onto v0.74.4, then lands |
| 7 | E1 angelus socket activation | fork tavern-culprit `df4d5475` (Engineer slot) | `lane/socket-activation` 5d80c21ad | SHIP (carrot-princess delta). Before landing, two LOWs: a tmpfiles `z` rule for angelus.sock so an idle switch applies socketMode; stopPid dies on EPERM rather than waiting. Lands in the train after v0.74.5 |
| 8 | P2 runtime.* migration | fork hydrant-rocket `78718b5c` | `lane/runtime-namespace` ffe9ba6d5 on P1 | READY; in one Opus review with P5 |
| 9 | E3 figaro valet and the fence | fork cognitive-backpack `3006ff4b` | `lane/valet` on b4bf913cb | building |
| 10 | P5 key-path grants, selectors, child_policy | hydrant-rocket | `lane/key-grants` 0783d56c8 | READY (default child writes its own self-class keys; seats take *:* per p5-migration.md); in review with P2 |
| 11 | P6 quota, role verbs, succession charging | after P3, P5 | `lane/quota` | queued |
| 12 | E4 house mode module (staged) | after E1-E3 | `lane/house-mode` + cheroot-config | queued; deploys only after Gluck's Merlin cutover |
| 12b | P7a reminder coalescing | fork fossil-anklet `6483d245` | `lane/reminder-coalesce` 5fb4c9345 | SHIP (buckle-hugeness); to the Knight for the next cut; Anthropic savings measured after release; P7b waits on P5 |
| 13 | P7 laws and reminder kinds | after P5 | `lane/laws` | queued |
| 14 | E8 the whole kingdom module | after P4-P6 | cheroot-config | queued |

## By morning (honest)

- Likely ready for the Engineer: IF1, P1, E0 (applies once `lane/managed-fanout` lands).
- Likely built, review may still be open: P4, E2, P3.
- If two more slots are granted: E1 built, P2 built and tested on a snapshot with its readers listed.
- Not by morning: P5, P6, P7, E3, E4, E8. Nothing is merged to main or deployed by the regiment; Merlin and spain are untouched.
