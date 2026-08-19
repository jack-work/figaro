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

Exception explicitly authorized for the final global installation:

1. snapshot every bound aria and every aria actively running a turn;
2. stop Angelus with `--keep-pids`;
3. install and restart;
4. verify all bindings restored;
5. fire-and-forget this recovery prompt to each aria that was active at
   shutdown: `continue, you were prematurely disconnected`;
6. verify each recovery prompt is scheduled and reaches a durable
   assistant/error completion.

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

### B12 - Forked fire-and-forget subagents may not activate

Deferred until the current durability stack is complete.

First-class expected workflow:

1. repeatedly fork the same live continuation with
   `figaro fork --id <parent> --stay -j`;
2. set each alternative's mantra;
3. dispatch work with `figaro send --id <alternative> -f -- "<prompt>"`;
4. allow every dormant alternative to restore, schedule, drain, persist an
   assistant response, and perform its filesystem work without requiring
   `listen`, `attend`, or a second foreground send.

Observed failure:

- explicit fire-and-forget prompts were durably visible as user messages;
- alternatives could remain `active` without filesystem work or recency
  updates;
- an alternative could become `idle` with no assistant response.

Acceptance:

- an explicit send to a dormant fork atomically restores/registers its Agent
  before the prompt is acknowledged;
- fire-and-forget means "do not stream to this client", never "persist without
  scheduling";
- repeated sibling alternatives from one running parent activate
  independently;
- status `active` reflects a real running/queued turn and cannot remain stale;
- every accepted prompt ends in a durable assistant/error `turn.done`;
- restart, concurrent fork, and eight-sibling stress tests cover this exact
  workflow.

### B13 - The subagents skill must distinguish child types

Deferred until B12 and the durability stack are green.

After both workflows are tested, update the installed skill at:

`~/.config/figaro/skills/subagents/SKILL.md`

It must teach two first-class child patterns:

1. **Context-sharing child**
   - Create with `figaro fork` when the child needs the exact canonical context
     and shared XWAL prefix of its parent.
   - Use `--stay` when the supervisor must remain bound to itself.
   - Set the child mantra/assignment metadata, then explicitly send its task.
2. **Isolated child**
   - Create a fresh aria when the child should not inherit the supervisor's
     transcript or provider cache prefix.
   - Stamp chalkboard metadata before or atomically with dispatch, including
     the supervisor aria ID and a child-kind/category value so the supervisor
     can list, group, and recover its children later.

The tested documentation must name one stable chalkboard key for supervisor
identity (candidate: `supervisor.aria_id`), show machine-readable ID capture,
avoid rebinding the supervisor shell accidentally, and explain cleanup/listen
semantics. Do not document commands that race task dispatch against metadata
assignment.

## Initial implementation decisions

1. A prompt received during an active tool round becomes a separate role-user
   IR message after the canonical tool-result message. Provider assembly may
   coalesce adjacent user messages, but canonical/UI ordering remains explicit.
2. Turn durability uses a dedicated XWAL channel of versioned full
   checkpoints. Every provider round gets a fresh turn generation. Recovery
   is idempotent.
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

## Figaro turn-durability implementation

Review status: **implementation-complete; final dependency and platform gates
remain**. Figwal is committed on the isolated workload branch at `e2f5c89`.
The complete Windows Figwal suite and vet are green; the deterministic
500-operation topology fuzz passed in 511.35 seconds with 189 trunks. Figaro's
full Windows tests, vet, and build pass through an external workspace pinned to
that exact Figwal worktree. Final review blockers below were fixed and covered
with focused regressions; post-pin Nix validation and controlled installation
remain.

- `turn-wal` is an opaque XWAL log channel with runtime `SyncManual`; opening
  the store idempotently installs it through `Trunks.EnsureChannel`.
- The store exposes a payload-agnostic `TurnJournal`:
  `Checkpoint(targetMainLT, payload)`, `Sync`, and `Latest(targetMainLT)`.
  Ephemeral arias do not open a journal.
- Checkpoint JSON is version 2 and contains `turn_id`, `generation`,
  `target_next_ir_lt`, `phase`, `partial_assistant`, ordered `tools`
  (`tool_call_id`, name, status, bounded output tail, error flag), and
  `timestamp_ms`. A provider seal also records `commit.assistant_lt` and the
  exact cache namespace, payload, and fingerprint.
- The drain loop performs the canonical assistant append. Provider goroutines
  write through a deferred log facade, so journal/live/IR ordering remains
  actor-owned. `PushFigaro` carries the exact provider-native cache candidate.
  The actor force-syncs the checkpoint, appends IR, appends and syncs the
  matching opaque cache entry, then acknowledges. Providers do not append
  assistant cache state themselves; cache append/open/sync failures propagate.
- Structural frames get a full write-ahead checkpoint. Prose/tool chunks
  checkpoint at most once per second; periodic writes sync. Interrupt/error
  forces a final checkpoint and sync. Therefore process death may rewind live
  frames emitted since the last periodic sync, bounded to less than one second.
- Recovery reads only the latest record at `IR tail + 1`. Assistant phase
  appends an aborted partial assistant and interrupted results for every ready
  call. Tools phase appends one ordered interrupted result message. Completed
  `ok`/`error` calls preserve their terminal output and status; only
  pending/running calls receive synthetic interruption errors. It runs
  before aria-server history construction and after actor panic, is idempotent,
  and never calls the provider. The tools checkpoint is written for the
  following LT before the assistant append; recovery that still sees the prior
  tail treats that one-record lookahead as assistant phase, closing the
  append-boundary crash window without a history scan.
- Translation caches moved to opaque `translations-v2/<provider>` channels;
  first open snapshots and migrates each provider's legacy bytes per lineage
  before channel installation can canonicalize the old channel. Payload and
  metadata/fingerprint bytes are copied unchanged. Unscoped, divergent, or
  incomplete migration is an explicit error.
- Successful canonical completion/recovery clears `turn-wal` on the addressed
  branch. Fork tests prove retirement does not clear an alternative lineage.
- Panic/seal-failure handling reconciles the aria server from canonical IR.
  Predicted assistant LT must equal the actual append LT.
- The turn journal is the transaction record spanning IR and provider cache.
  Recovery completes journal-only, IR-only, and IR-plus-cache states
  idempotently without provider recall. Direct Anthropic native capture, SDK
  signed thinking, Responses encrypted output, fork-at-seal, and cache append
  failure are covered.
- Existing distinct-message live steering ordering remains unchanged and is
  covered by the full test suite.

## Status

| Item | State |
|---|---|
| Diagnostics and isolated reproductions | complete |
| Branch creation | complete |
| Consolidated ledger | complete |
| Live-send ordering fix | complete (pre-existing commit, preserved) |
| Figwal journal API | complete on workload commit `e2f5c89`; full suite/vet green |
| Figaro partial-turn persistence | complete; focused review regressions green |
| Fork timeout/cancellation UX | fixed false 10-second timeout; protocol-level ambiguity remains |
| Skill folder support | complete |
| Forked fire-and-forget subagent activation | deferred follow-on after durability installation |
| Two subagent kinds in installed skill | deferred until both workflows are verified |
| Crash/interrupt/restart tests | focused and full Windows suites green |
| Full Nix/race/build validation | complete: Nix build/check green; affected Linux race suites green; native Windows race unavailable |
| Annotated commits | Figwal and Figaro workload commits complete |

## Resolved final-review blockers

1. Figwal deep-fork repair is cached and bounded. The complete non-fuzz suite
   passes, and `TestForest_FuzzSequential` passes in 511.35 seconds under the
   600-second gate.
2. Terminal tool states are force-synced before canonical tool-result append;
   a backend ordering regression rejects the old crash window.
3. Assistant-phase recovery syncs a one-record-ahead tools handoff before
   appending aborted assistant IR, preserving completed/error tool output
   across a second crash.
4. Startup fails closed on native-cache recovery errors and skips legacy
   dangling-tool repair, so unrecovered exact cache commits cannot be hidden by
   advancing IR.
5. Direct Anthropic accumulates `signature_delta` and `redacted_thinking`
   provider-native fields and commits sanitized input-ready native bytes rather
   than reconstructing them from provider-agnostic IR.
6. Legacy-to-v2 migration verifies payload and metadata/fingerprint bytes for
   every migrated prefix record, not only count and logical time.
7. Panic recovery preserves the existing inbox; accepted queued prompts and
   coordinated forks retain FIFO order and complete after actor restart.

## Remaining gates

1. Push the annotated Figaro workload branch.
2. Perform the authorized controlled install: snapshot active/bound arias,
   preserve PID bindings, restart, verify bindings, and resume interrupted
   active arias.
