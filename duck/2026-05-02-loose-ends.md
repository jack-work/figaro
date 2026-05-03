# 2026-05-02 — Loose ends

**Status:** standing checklist. Things we said we'd do but haven't, and one
documented bug. Keep concise; tick items off (or move them to `plans/`)
when they ship.

## Pending

- [ ] **Prefix byte-stability regression** — restore the test deleted in
      Stage C.3 (`internal/provider/anthropic/prefix_stability_test.go`).
      What survives today is the weakest form (single-call same-input →
      same-bytes). Need: multi-turn projections with chalkboard mutations,
      bootstrap entry + tool-result tics, and the new skills system block,
      all asserting byte-equality of `req.System ++ req.Tools ++
      req.Messages[:len-1]`. Without this, a future change can silently
      degrade Anthropic's `cache_read` tokens — runtime cost shows up as
      bills, not failures.
- [ ] **CHANGELOG entry** for the Stage A → E rollout (storage migration,
      IR shape change, Translation rename, parallel translation file,
      skills move).
- [ ] **`plans/cache-control/SYSTEM-REMINDERS.md`** — stamp `Status:
      implemented`; the design landed via Stage C/D/E with a different
      architecture than the original plan. Same review pass for
      `plans/cache-control/prompt-caching-limitations.md` (still
      accurate; just confirm).
- [ ] **`plans/ponder-points/`** — README references successor work
      "unchanged from v2"; structural prerequisites are now delivered, so
      the ponder-points design can move forward when prioritized.
- [ ] **TranslationEntry `Kind` discriminator** — see § Bug below. The
      role-validation fix in `b7db923` is sufficient for now, but a
      proper Kind field (`"message"` / `"system"` / `"tools"`) would
      make the lookup unambiguous and avoid further role-of-shape
      collisions when new entry kinds land.
- [ ] **Translator prototype** — see
      `2026-05-02-translator-peers-streams-summaries.md`. Build standalone
      with mock streams, evaluate, then decide where it slots in.
- [ ] **`AriaMeta` retirement** — currently lives in
      `arias/{id}/meta.json` as a "transitional" artifact (see
      `internal/store/file.go`). Stage C.6 was supposed to retire it in
      favor of `chalkboard.system.*` taking over restoration metadata.
      Today still load-bearing for `RestoreArias`. Item from the v3 plan,
      not yet done.
- [ ] **Hush passphrase TTY wall** — non-TTY shells (Claude Code's Bash
      tool, CI) can't supply the passphrase, blocking live verification.
      An env-var-driven dev-only escape hatch would unblock automated
      testing.
- [ ] **`figaro plain` output capture** — verify scripts using `tail -1`
      to capture plain output produce noisy / truncated results;
      probable issue with how `plain` flushes its final newline or
      cursor codes. Worth a closer look during dedicated UI testing.

## Bug — documented, fix pending the proper Kind discriminator

### Cache lookup admitted non-message-shaped translation entries (FIXED)

**Surfaced:** Stage E live verification (2026-05-02). Anthropic API
returned `400 messages.0.role: Input should be 'user', 'assistant'` on
the second turn of an aria.

**Cause:** Stage D.2f's write-through translation persistence introduced
log entries for the system block array, keyed by the *bootstrap* figaro
flt (the same flt that the per-message lookup uses for index 0 of the
block). The cache-hit path in `projectMessages` used `json.Unmarshal` to
decode any cached entry as a `nativeMessage`. Lax JSON decoding turned
the `systemBlock`-shaped bytes (`{"type":"text","text":"…"}`) into
`nativeMessage{Role: "", Content: nil}`, which the API rejects.

**Fix:** validate `cached.Role == "user" || cached.Role == "assistant"`
before treating an entry as a usable per-message projection. Lands in
commit `b7db923`. Regression test:
`internal/provider/anthropic/projection_test.go::TestProjectMessages_IgnoresNonMessageCachedEntries`.

**Why a proper fix is still owed:** the role-validation check is
defensive but doesn't solve the underlying conflation. As more entry
kinds arrive (tools array, future per-peer entries), each will need its
own validation rule and the lookup logic will sprawl. A `Kind` field on
`TranslationEntry` (`"message"`, `"system"`, `"tools"`) would let the
agent's `buildPriorTranslations` filter at construction time, returning
an empty entry for any non-message kind without the provider needing to
defend itself. Listed in § Pending above.
