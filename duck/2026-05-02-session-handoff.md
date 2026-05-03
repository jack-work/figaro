# 2026-05-02 — Session handoff

**Status:** brainstorm / handoff. Written by Claude before the session
closed, for whoever picks up next. The signal here is "what does the next
agent benefit from knowing that isn't yet in the code or in `plans/`"; the
noise has been pruned.

## Where the project is right now

**Branch:** `main`. All Stage A → E work has been pushed. `git log -1`
should show `dc1c148` (the user's `feat(ql)` flake commit) at the head, or
later if more landed in the meantime.

**Recently completed (in commit order):**

- Stage A — `Patch` relocation; `Translation` envelope (formerly Baggage); new content-block shapes.
- Stage A.5 / B — `ContentToolResult`; on-disk move to `arias/{id}/aria.jsonl + meta.json`.
- Stage C.1–C.6 — `*chalkboard.State` per-aria handle; bootstrap state-only tic; `figaro.rehydrate` RPC + CLI; trimmed credo `Context`; agent loop accumulates an in-progress tic; provider renders patches inline as `<system-reminder>` content blocks.
- Stage D.1–D.2f — chalkboard snapshot threaded through `Provider.Send`; `Translation` renamed (Baggage → Translation); `causal.Slice[T]` / `causal.Sink[T]` package; `Provider.Fingerprint()` + `OpenAccumulator()` interface; translations moved to a parallel `arias/{id}/translations/{provider}.jsonl` file (timeline of `{alt, figaro_lts, messages, fingerprint}`); `Translation` field dropped from `message.Message`; regenerate-on-Fingerprint-mismatch; **write-through translation persistence on Send** (provider returns `ProjectionSummary`, agent writes per-message + system-block bytes, idempotent skip).
- Stage E — skills out of the credo body, into `chalkboard.system.skills` as a structured catalog. Provider emits skills as a separate system block. Bug fix in the same commit: cache lookup in `projectMessages` was admitting non-message-shaped translation entries (the system block array stored at the bootstrap flt) as user messages, producing empty-role wire bytes. Now validates `cached.Role` before use.

**Not yet done:**

- Stage F (docs / benchmarks / CHANGELOG / `agents.md` polish). The
  `agents.md` updates I made in 2026-05-02 are partial — a thorough
  pass for `ARCHITECTURE.md` is still owed.
- `plans/aria-storage/log-unification.md` was the v3 plan that drove
  Stage C / D / E. Its "deliverables" sections are now historical;
  consider stamping `Status: implemented (D.2f / E)` and pointing at
  the relevant commits.

## The big in-flight design discussion

**Read first:** `duck/2026-05-02-translator-peers-streams-summaries.md`.

That brainstorm captures the user's vision for a translator-as-first-class
component — all parties (LLM, CLI, future telegram/webui, even tools) as
*peers* with their own IR, the figaro IR as the canonical merge point, and
a two-tier stream model that distinguishes volatile streaming events from
durable summarized messages. The user has answered each of the open
questions Claude raised at the bottom of that doc — those answers are
load-bearing if you start prototyping the translator.

Highlights from the user's answers (paraphrased; the full text is in the
brainstorm):

- **Naming:** `encode` / `decode` for the directions. (Settles point #1.)
- **Summary triggers:** practical examples — CLI sends one message per
  aggregation; Anthropic emits a "summarize" event that drives both sides.
  Triggers come from peers, not from internal heuristics.
- **Cursor durability:** **NOT** persistent across restarts (revising
  Claude's recommendation). Post-summarized messages are considered
  already-visited by handlers; on summary, all peers receive a hook.
  Volatile content lost on process death is acceptable for now. Future
  work item.
- **FK collapse on summary:** intentional information loss. Document but
  don't try to fix.
- **Tools:** more specialized than a generic peer — "environment" peer.
  Built-ins, share security boundary, native figaro communication. The
  system should support configuring environments via the chalkboard
  (e.g. an SSH-backed environment, or an IPC process pool). When the
  figaro IR is longer than peer streams in tail translation, that
  implies environment updates the peers need to be notified about.

The user's stated implementation strategy: **build the translator
standalone, with mock streams, evaluate it, and only fold it in once the
abstractions prove themselves.** Don't replace the agent loop wholesale.

## What the next agent should do (tentatively)

If the user says "continue the translator work":

1. Sketch the Go API for the tail translator in a new package
   (`internal/translator/` or, given the naming-collision concern, maybe
   `internal/peerbridge/` or `internal/irbridge/`).
2. Define `Peer` (the abstraction every peer adapter implements: its
   IR type, encode/decode, cursor placeholder).
3. Build a deterministic eval harness — canned event sequences, assert
   correctness of resulting streams, measure throughput.
4. Make the volatile/summarized boundary explicit early. The trickiest
   correctness questions live there. The user is comfortable with
   "summary-only durability" for now; prototype that first.

If the user says "land Stage F" or "polish docs":

1. `ARCHITECTURE.md` is overdue for a Stage C/D/E sweep — it still
   describes the pre-D.2 shape in places.
2. `plans/aria-storage/log-unification.md` should get `Status:
   implemented` notes and pointers to the implementing commits.
3. CHANGELOG entry for the Stage D / E rollout.
4. The prefix-byte-stability regression test was mentioned in the v3
   plan but never extended to cover bootstrap + tool-result tics.

If the user says something else: read the brainstorm doc, read this doc,
read `agents.md`, then ask.

## User collaboration patterns I observed

These are inferred from a long session; treat as priors, not rules. If
the user contradicts one, defer to them.

- **Forward-only refactors in dev mode.** When a shape changes, the user
  prefers deleting old data over carrying a dual-read path. They've said
  this explicitly: *"in general, we can always prefer the new shape,
  rather than support old shapes."*
- **Stage commits are sub-staged.** Big stage = letter (C, D, E). Each
  letter is broken into sub-stages (D.2a, D.2b, …, D.2f). Each sub-stage
  is one self-contained commit. The tree stays green at every commit.
- **Live verification matters.** When the user wants to validate work,
  they want to actually run figaro and see it behave — not just unit
  tests. The `/tmp/verify_figaro*.sh` pattern (install, restart angelus,
  drive a representative conversation, inspect on-disk state) is what
  works. Skills and tool-using prompts make the test richer.
- **Comments explain WHY, not what.** The user has corrected this
  multiple times in agents.md (§ Conventions) and again in conversation.
  When introducing scaffolding for future work, leave a breadcrumb
  pointing at the future direction so a reader doesn't think it's
  stranded.
- **ASCII art for architectural communication.** When the user wants to
  understand a multi-component flow, drawing it works better than
  paragraphs. They asked for it explicitly during the translator
  discussion.
- **Idempotency is preferred over locking.** Skip-on-match writes,
  fingerprint-driven invalidation, append-only logs that get replaced
  rather than mutated. The user thinks this way naturally.
- **Brainstorms get auditable lossless capture.** This `duck/` directory
  is the user's convention. When they think out loud, they want their
  reasoning preserved in their voice, not paraphrased into a synthesis.
  Feedback should be clearly separable from the original thinking.
- **Nix-canonical install.** The user removed the go-installed binary
  and uses `nix profile install .#figaro`. The flake's `q` and `l`
  multi-call symlinks are part of the deliverable.
- **`/loop` and autonomous runs welcome.** During Stage C/D the user
  said *"please proceed beyond my wait limit"* — they're comfortable
  with long autonomous runs as long as the tree stays green and each
  commit is reviewable. They will catch up at their own pace.
- **Hush passphrase blocks live verify in non-TTY shells.** If you can
  build but can't run the binary because hush wants a passphrase, say
  so explicitly. The user can unlock and you can re-run. Don't fake the
  verification.

## Practical entry points

- **Read in this order:** `agents.md` → `ARCHITECTURE.md` →
  `duck/2026-05-02-translator-peers-streams-summaries.md` →
  `plans/aria-storage/log-unification.md`.
- **Run the test sweep first:** `go build ./... && go test ./... && go vet ./...`.
  All green at handoff.
- **Smoke-test a real prompt:** if the user has hush unlocked, run a
  tool-using prompt (`figaro plain -- "use bash to print the date"`) and
  inspect the resulting `~/.local/state/figaro/arias/$ID/` to confirm
  the four-file layout (`aria.jsonl`, `chalkboard.json`, `meta.json`,
  `translations/anthropic.jsonl`).
- **Memory:** `~/.claude/projects/-home-gluck-dev-figaro-qua-main/memory/`
  is Claude Code's persistent memory directory. The user has been
  letting it accumulate naturally; nothing critical lives there yet.
