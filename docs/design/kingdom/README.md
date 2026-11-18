# Kingdom: status board

One Strategist plans both tracks and fans out forks that build parts. The Knight carries non-conflicting changes, produces the validated build and asks the Review Steward for a reviewer. Only the Engineer merges.

- Plans: [permissions.md](permissions.md) (Track P) and [estate.md](estate.md) (Track E). Deployment modes, revision 2: keel `proposal/deployment-modes` 480ce9e4a.
- Seats (roles; their names live in the charter, never in figaro Go): Figaro Strategist `@9bf485c2`, Figaro Knight `@d121c404`, Figaro Review Steward `@9e5bdab3`. Reviewers are turn-1 forks of the Strategist on Gemini, one per change.
- Validated builds: `/var/tmp/kingdom-builds/<part>`.

## The whole plan (14 items), board order

Working agreement: REVISION 2 of the plan root. The Strategist builds everything at once with its forks, keeps the final vision, and restacks it into non-conflicting commits for the Knight as main moves. Reviewers fork the Strategist at turn 1 (the common fork point) on Gemini.

**Running through the day (Gluck 10:0x: until all of it ships; no quota pauses).** Strategist forks, 7 of 7:
- P4: varied-username `f0fbbf89`
- E1 LOWs, then E3: llama-widow `937e1be4`
- P5 finish (E8 findings), then the fold: `eb4da540` (took over from blouse-pretzel; handoff /var/tmp/kingdom-part-p2p5-handoff.md)
- P6: keepsake-avalanche `874637fe`
- P7b laws: urchin-yacht `ee3ef672`, READY at `lane/laws` 694f07b16 on P5; folds after P5 (fork-head number to settle)
- E4 house mode: utensil-slapping `ef74d1d6`
- S1 auto-normalize: PARKED at `lane/auto-normalize` 67af48da5 (harvest kingdom-s1-auto-normalize.md); restacked last by a fresh fork
- E8 kingdom module: cheroot-config `lane/kingdom-module` 45fa9c1, SHIP (ocular-epidemic), staged with El Mayor; lands after the P5+P6 release. Follow-ups: drop read:aria:@role once P5 resolves roles on the daemon side; rerun seats-e2e on the P5 build; p5-migration.md lands with P5

Under the Engineer: composed Seat plan, neither-blimp `a107c3b5` (`lane/seat-plan` on rel/v0.75.0). Optimistic fork part 1 (the store's birth lock), iphone-sushi `7c36bb41` (`lane/optimistic-fork`). Part 2 (admission off the Hierarchy mutex) waits on P6.

v0.74.5 review FIX, the Knight fixing (F-A cap overfill on kill+restore, plus F-B to F-G); P2+P5 in review (hyacinth-obtain).

**North star (Gluck 11:2x): keel `kingdom/north-star`, now at b877e4faf (= rel/v0.75.0: fixed v0.74.5 + E1 + P2 + the boundary fix).** One integration branch for the whole remaining plan.
- **Folding:** a lane rebases onto the north-star tip and gates there; then the Strategist fast-forwards. The branch stays linear, one idea per commit.
- **Review in post (Gluck 11:4x).** A slice ships on a green gate plus the Knight's Sonnet slice check. Folds don't wait on Opus. listless-oncoming `92aa6e78` (Opus, warm) reviews each shipped batch afterwards, and its findings ship as follow-up patches the same day, High first.
- **Fold order as lanes are READY:** E3, P4, P5 (fix), P6, P7b, E4, reaped-budget, fork-latency, seat-plan, optimistic-fork. E8 is cheroot-config, and S1 comes last.

**Release rules (Gluck 13:3x, for the rest of the drive):**
1. Only High findings block a ship. Medium and Low become tracked follow-ups (an issue or the next lane), named in the notes when user-visible.
2. A fix round with a sabotage-checked test per finding gets the Engineer's diff read, not a delta review. A full delta review only when a High's fix redesigns something.
3. Opus reviews only authz, hierarchy, store and hush; Sonnet reviews the rest.
4. A green gate stays mandatory.
5. Any breaking change ships in a minor.

Train: **v0.75.0** = fixed v0.74.5 (P3+H1+P1b) + E1 + P2 (rel/v0.75.0 a07b06838), one minor and one restart. Later lanes go together on one cut whenever that is faster. S1 ships last.

| # | part | builder | branch | state (2026-11-18 02:xx) |
|---|---|---|---|---|
| 1 | IF1 study_incantation string | Knight | `lane/study-incantation-string` | in v0.74.3 (cutting) |
| 2 | P1 key registry | Knight | `lane/key-registry` 63de1c6f4 | SHIP (coverless-lullaby); P2/P5 restack on it |
| 2b | H1 place --under; succeed --successor drawn in place | fork oceanic-degree `a78f3129` | `lane/hierarchy-place` | `lane/hierarchy-place` a40686c8d on P3 | READY; ships with P3 as one stack in v0.74.5, one batched review after v0.74.4 |
| 2d | F2 owner check ignores the figaro claim on the local door (pre-existing; cap bypass, role hijack) | Knight | off 0911f728f | review FIX (aardvark-amazingly): scoped demote/succeed regression being fixed; v0.74.4 = F2 + that fix + callerOwns local door + dead-claim-as-owner + consent leak; delta review next. Unscoped topology reach moves to P5 |
| 2c | P1b shape checks, figaro doctor keys | Knight | `lane/key-shapes` bf12f271f on 63de1c6f4 | SHIP (academy-kite, all A); folded into v0.74.5 by the Knight; P5 restacks on it and calls the shapes |
| 3 | E0 kingdom fan-out (charter `maxChildren`) | fork tavern-culprit `df4d5475` (drawn under the Engineer) | cheroot-config, off `proposal/system-charter-split` | finishing |
| 3b | R1 kingdom regiment | Knight | cheroot-config `lane/kingdom-regiment` e86f3dc (on 579193c) | SHIP (explode-nugget, no findings); owner lands eced4cd, ac170c8, e86f3dc, then flake.lock with f11866f + 579193c |
| 4 | P4 peer caller check | fork anagram-erratic `0e15cab0` | `lane/peer-caller-check` de5d64574 | fixes in (finite ttl from tools, succeed carries the seat ttl, peer-check.md); waits on v0.74.4 to restack, then review |
| 5 | E2 hush as a system service | fork erasable-unzip `56faa8d6`; drop-box refresh by the Strategist | hush `lane/hush-system-service` 70d3782 + `lane/hush-drop-refresh` | SHIP; landing branch hush lane/hush-landing e2497c5be (gate green) with the Engineer to merge |
| 6 | P3 admission ledger | fork dominoes-elusive `439448d3` | `lane/admission-ledger` a7ec40e88 | restacked on 0911f728f with the shared seat op (figaro.seat); restacks onto v0.74.4, then lands |
| 7 | E1 angelus socket activation | fork tavern-culprit `df4d5475` (Engineer slot) | `lane/socket-activation` f7a95a444 | SHIP plus the two LOW fixes; with the Knight for v0.74.6 |
| 8 | P2 runtime.* migration | fork hydrant-rocket `78718b5c` | `lane/runtime-namespace` 2dd22739c on v0.74.5 | SHIP (hyacinth-obtain); with the Knight for v0.74.6 |
| 9 | E3 figaro valet and the fence | llama-widow `937e1be4` | `lane/valet` 0920abcba | READY; Opus review listless-oncoming `92aa6e78`; restacking onto v0.75.0 |
| 10 | P5 key-path grants, selectors, child_policy | hydrant-rocket | `lane/key-grants` 4d7c45342 | review FIX (F1 succeed strips the seat, F2 outfits dressed after checks, F3 study/cast via keyed set, F4 glob subsumption, F5 local-door label, F6-F8); blouse-pretzel fixing, delta review next |
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
