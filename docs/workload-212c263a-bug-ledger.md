# Workload 212c263a bug ledger

Workload: `212c263a-64b4-47af-9570-95702e164055`

This document is the durable source of truth for every bug report and
constraint raised in this workload. Update it as findings become fixes. Do not
rely on conversation history surviving compaction.

Branches:

- Figaro: `workload/212c263a-turn-durability`
- Figwal: `workload/212c263a-turn-durability`
- Figwal base: `perf/native-xwal-cache`

## Permanent safety constraint

Do not stop, restart, signal, reconfigure, replace, or install over any running
Windows Figaro. All runtime tests use isolated NixOS state, runtime, config,
cache, and hush paths.

## Reported bugs

### B01 - New aria creation becomes very slow

Observed live `angelus.create` latency reached tens of seconds. Create and
unrelated channel writes serialize behind Figwal's global trunk mutex, while
XWAL repeatedly reopens and scans active segments.

Acceptance:

- unrelated lineages do not block one another;
- create latency is not proportional to total topology/history;
- normal translation catch-up remains O(untranslated entries).

### B02 - Figaro can appear lost after daemon loss

PID bindings are only saved by explicit `stop --keep-pids`. A restart without
that file makes the next prompt create a new aria while the old durable aria
becomes dormant and hard to rediscover.

Acceptance:

- bindings survive graceful and abrupt daemon replacement as far as atomic
  local persistence allows;
- Windows PID reuse cannot bind the wrong process;
- recovery never silently creates a replacement when a valid persisted
  binding exists.

### B03 - Memory, handle, listener, and actor retention

Idle Agents are never unloaded. `Agent.Kill` does not stop the per-aria socket
because the listener uses the daemon context. Full histories and provider
projections remain cached.

Acceptance:

- killing an Agent releases its listener, FD, goroutines, and socket path;
- idle unbound Agents can become dormant without deleting durable history;
- backend cache handles have a non-destructive release path.

### B04 - An aria does not reliably know its own ID

Create stamps the legacy `aria_id`, but fork alternatives inherit the parent
value. Users/loadouts can also overwrite it.

Acceptance:

- system-owned `identity.aria_id` is visible from the first request;
- a fork alternative receives a normal state transition to its new ID;
- continuation identity remains stable;
- user/model/loadout patches cannot set or remove identity.

### B05 - GPT-5.6 Sol context fills rapidly

Figaro sends the complete projected history on every stateless Responses
request. Live Sol histories are dominated by tool-result text. No compaction
path exists even though limit errors tell the user to compact.

Acceptance:

- canonical tool results are context-bounded and recoverable through an
  explicit artifact/page mechanism;
- Responses may use persistent-socket `previous_response_id` for latency, but
  full local replay remains the correctness fallback;
- context compaction has explicit canonical/durability semantics;
- Anthropic and Responses context metrics use provider-correct token totals.

### B06 - Provider cache prefixes must remain byte-stable

Live GPT and Anthropic-Messages prefixes are byte-stable where entries persist,
but dynamic translation channels fail for later loadouts and most Sol arias
have no durable Responses cache. Clean Nix tests also proved that the JSONL
codec recursively canonicalizes nested provider JSON while forking: alternative
branches reorder opaque request-object keys and therefore change prefix bytes.

Acceptance:

- later stumps/trunks mirror every translation channel;
- opaque provider-cache channels preserve payload bytes exactly across write,
  reopen, tail fork, interior fork, and sibling shared-prefix reads;
- fork/restart/model-switch tests prove existing cached prefix bytes do not
  change;
- opaque reasoning/signature/phase fields survive the provider-specific path;
- cache failure never silently advances durable state.

### B07 - GPT-5.6 tool-heavy work is hard to follow

The default loadout requests high effort but no readable reasoning summary.
The user explicitly deferred adding a required `purpose` parameter to bash,
read, write, or edit.

Acceptance:

- first evaluate `reasoning_summary`, `reasoning_context`, verbosity, and one
  concise pre-tool-batch narration rule;
- expose readable summaries, never raw chain of thought;
- do not add a tool `purpose` field in this workload.

### B08 - `fig send` messages appear inside older listen blocks

Prompts arriving during a tool round are currently folded into the same
role-user message as tool results. The transcript renderer then displays the
steering text beneath older tool nodes, making wire order appear corrupted.

Acceptance:

- each externally submitted prompt is a distinct canonical role-user IR
  message;
- it is ordered after required tool-result blocks and before the next provider
  round;
- listen/transcript notification order matches canonical IR order;
- no prompt is duplicated or lost when it arrives during provider or tool
  execution.

### B09 - Interrupt/restart can lose the whole in-progress turn

Streaming assistant/tool state lives only in memory until a sealed message is
appended to canonical IR. Interrupt or process loss can discard visible output.

Acceptance:

- XWAL records in-progress turn state before it is published as durable live
  output;
- periodic checkpoints bound replay work;
- canonical commit records retire the in-progress journal state;
- interrupt seals a valid partial turn exactly once;
- restart recovers the last durable partial assistant/tool state;
- incomplete tool calls receive provider-valid interrupted results;
- forks never copy mutable in-progress state into the wrong lineage.

### B10 - Fork can time out while still completing

The CLI applies a fixed 10-second timeout, but the server ignores the request
context while a coordinated fork waits in the actor inbox or mutates storage.
The client can report failure while the fork is created later.

Acceptance:

- a fork canceled before actor execution is not created;
- once storage mutation starts, the CLI waits with visible progress instead of
  reporting a false timeout;
- the actor services ready forks at every safe provider/tool/checkpoint
  boundary;
- per-lineage Figwal writes and hot topology keep normal forks comfortably
  below the interactive timeout budget;
- retry after an ambiguous transport failure cannot create duplicate
  alternatives.

### B11 - Skills need first-class folder support

Skills should be loadable as either one top-level Markdown file or a directory
containing `SKILL.md` plus supplemental files. Symlinked skills should point to
the directory, not only to one file.

Current code has partial directory support, but the starter asset and docs
still present skills as top-level files, and user-symlinked directory behavior
is not pinned by tests.

Acceptance:

- `skills/foo.md` remains supported;
- `skills/foo/SKILL.md` and `skills/foo/skill.md` are supported;
- a symlinked `skills/foo` directory loads as one `skills.foo` entry;
- `filePath` points through the skill directory so referenced supplemental
  files remain readable;
- supplemental files are not surfaced as independent skills;
- first-run scaffolding creates `skills/howto/SKILL.md`;
- bundled/user override semantics remain unchanged.

## Initial implementation decisions

1. A prompt received during an active tool round becomes a separate role-user
   IR message after the canonical tool-result message. Provider assembly may
   coalesce adjacent user messages, but canonical/UI ordering remains explicit.
2. Turn durability uses a dedicated XWAL channel as an append-only journal.
   Records include begin/checkpoint/commit/abort identity and a turn
   generation. Recovery is idempotent.
3. The journal stores bounded periodic full checkpoints plus write-ahead
   structural updates. It must not restore the removed per-frame `_live` blob
   or perform full-history scans.
4. User-confirmed recovery contract: a recovered partial turn is sealed as
   interrupted canonical IR, restored visibly, and then waits for the next
   prompt. Network streams are not assumed resumable after process death.
5. The Figwal branch must first preserve per-lineage serialization, hot heads,
   shared fork prefixes, and dynamic channel correctness from its WIP base.
6. Commits include:

   `Workload: 212c263a-64b4-47af-9570-95702e164055`

## Status

| Item | State |
|---|---|
| Diagnostics and isolated reproductions | complete |
| Branch creation | complete |
| Consolidated ledger | complete |
| Live-send ordering fix | pending |
| Figwal journal API | pending |
| Figaro partial-turn persistence | pending |
| Fork timeout/cancellation UX | pending |
| Skill folder support | pending |
| Crash/interrupt/restart tests | pending |
| Full Nix/race/build validation | pending |
| Annotated commits | pending |
