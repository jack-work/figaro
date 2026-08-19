# Figaro vs Claude Code — daily-driver gap analysis
# (produced by figaro aria ffda75ad, 2026-07-24)

I have plenty. Let me deliver the analysis.

---

*Ecco — a survey with the razor, not the scissors. Analysis only, on va.*

# Figaro vs. Claude Code: Daily-Driver Gaps

Reference points: `internal/cli/cli.go` (verb surface), `internal/rpc/methods.go` (wire), `internal/figaro/{agent,turn,inbox}.go` (per-aria actor), `internal/angelus/protocol.go` (supervisor), `internal/tool/{registry,tool}.go` (tool contract), `agents.md` (invariants).

Figaro already has, and I'll stop crediting these: append-only IR, forking, chalkboard-as-system-reminders, per-aria socket, `figaro.interrupt`, mid-turn steering as inbox-queued user prompts (`inbox.TakeReadyUserPrompts`), `turn.done{idle}` semantics distinguishing "settled" from "steer still queued," lazy-restore of dormant arias on angelus reboot, full-screen transcript pager (`internal/cli/transcript.go`), incremental usage/metrics in `AriaMeta`, and parallel aria orchestration via bash + `figaro send -f -j`.

What's missing, ranked. **Must-have** = a Claude-Code daily driver would immediately feel the absence; **nice-to-have** = you'd notice on the second week.

---

## MUST-HAVE

### 1. Tool permission / approval modes  *(the biggest gap)*
**Status in figaro:** none. `internal/tool/tool.go` defines `Execute(ctx, args, onOutput)` — no gate, no policy hook. Every tool call runs. The only user brake is `figaro.interrupt` **after** the fact.

**Why it matters:** Claude Code's *default → acceptEdits → bypassPermissions → plan* toggle, per-tool allow/deny prompts, and the `.claude/settings.json` allow-list are load-bearing for real work. Without them, figaro cannot be trusted with `bash` in a directory you care about. Right now the risk is amortized only by the credo and by the `-y` skip-confirm on `figaro send -x`.

**Sketch:**
- Add `system.tool_policy` to the chalkboard (`plan | ask | accept-edits | bypass`) + per-tool overrides (`system.tool_policy.bash = "ask"`, glob rules for `bash` commands).
- In `internal/figaro/turn.go` where the dispatcher fires each `tool_invoke`, insert a policy check that can produce three outcomes: `execute`, `execute-and-remember`, `deny-with-message` (a synthetic `tool_result` with `denied by policy`). This is where the actor lives, so ordering is safe.
- Approval prompts flow over a new notification (`tool.approval.request {tool, args, id}`) and a new request (`tool.approval.decide {id, decision, remember}`). The CLI painter (`internal/cli/stream.go`) grows a modal row; `send -f`/non-TTY sessions default to deny-with-diagnostic.
- Wire: new `rpc.MethodToolApproval{Request,Decide}` methods. Persist "remembered" decisions as chalkboard patches so they replay on restart and are visible via `figaro state`.

### 2. Lifecycle hooks (PreToolUse / PostToolUse / Stop / Notification)
**Status in figaro:** none. Grep for `hook` returns three unrelated comments.

**Why it matters:** Claude Code's hook system is what people wire into `just fmt`, secret scanning, audit logs, ntfy pushes, git-guardrails, `gofmt` on write. It converts figaro from "one binary" into "policy substrate for a team." It also gives you the seam for permission decisions (#1) without hard-coding UI.

**Sketch:**
- New chalkboard family `system.hooks.<event> = ["shell command", ...]` where `event ∈ {pre_tool_use, post_tool_use, turn_start, turn_done, notification, subagent_done}`.
- Hook runner in `internal/figaro/turn.go` — a helper that fans out configured commands, feeds them JSON on stdin ({tool, args, aria_id, cwd, patches}), and gates on exit code (`0=allow`, `2=deny with stdout as message`, matching Claude Code's convention). Sync, timeout-bounded, results go through the inbox to preserve invariant #1 (single-owner).
- Because the actor is already inbox-serialized, a hook is just another event with a completion channel — same pattern as `CoordinateFork`.
- The angelus doesn't need to change. The runner is per-aria.

### 3. Queued-message visibility (type-ahead UI)
**Status in figaro:** the inbox already queues (`figaro/inbox.go:queue`), and `turn.done.idle=false` already tells the client "still work pending." But the client never displays what's queued: `stream.go` renders live frames, not an inbox summary.

**Why it matters:** Claude Code shows every pending user message stacked under the composer with edit/delete affordances while the assistant streams. You *know* what's about to fire; you can retract. In figaro you can `figaro send` twice mid-turn and forget you did.

**Sketch:**
- New wire event `inbox.state {queued: [{id, kind, text_preview, submitted_at}]}` emitted whenever `inbox.queue` changes shape (add/take/drop). Cheap — the inbox already has the mutex.
- New request `figaro.dequeue {id}` that removes a specific queued prompt if the actor hasn't yet drained it.
- The painter grows a small "pending →" region above the live tail. Only rendered on TTYs.
- Bonus: reuse this for a `figaro inbox` verb that dumps the queue for scripts.

---

## STRONG NICE-TO-HAVE

### 4. Plan mode
**Status in figaro:** no read-only mode; tools all execute the moment the model calls them.

**Why it matters:** Claude Code's *plan mode* is really "run only side-effect-free tools; produce a plan; wait for user approval." It's how you safely turn a large model loose on a repo without pre-committing to edits.

**Sketch:** Falls out of #1 almost for free. Add a tool-classification field to `tool.Tool` (`Effects() int` → read | edit | exec | net) or a static registry (`bash/write/edit = mutating; read/process.log = readonly`). Plan mode = `system.tool_policy` preset that denies everything mutating with the message *"return a plan text block only."* Add a single-key toggle (Ctrl-P in the transcript) to flip it, plus `figaro plan` / `figaro accept-edits` verbs that just `figaro set system.tool_policy …`.

### 5. Background-task / notification-on-idle signals
**Status in figaro:** bash auto-backgrounds long commands (architecture.md notes this) and `turn.done` fires — but nothing tells the user their tab is ready. No bell, no `notify-send`, no ntfy.sh push.

**Why it matters:** With multiple arias (`bossman`, `parallel-arias` skills exist for a reason), the operator's eyes cannot be everywhere. Claude Code's Notification hook + terminal bell on idle is what makes fan-out ergonomic.

**Sketch:** trivial once #2 (hooks) exists — ship a first-party `notification` hook event fired from `endTurn` and from bash's auto-background handoff. Optionally a built-in fallback (`system.notify = "bell" | "notify-send" | "off"`) so users get something out of the box before writing hooks. Emit BEL only for interactive PTY clients, not raw/JSON.

### 6. Slash-commands / user macros
**Status in figaro:** loadouts + skills are close but coarser. There is no per-invocation `/refactor` macro that expands to a canned prompt with slots.

**Why it matters:** Claude Code's `.claude/commands/foo.md` becomes `/foo` at the composer. It's the muscle memory. In figaro today you either write a shell alias or a skill.

**Sketch:** Reuse the loadout loader (`internal/outfit`). New `commands.d/<name>.md` scanned per-cwd + `~/.config/figaro/commands/`. A prefix (`;name arg1 arg2` — colon is taken by `:skills.foo!` and `:LT`) at `figaro --` expands to the file body with `$1..$N` substitution before hitting `figaro.qua`. Completion via `completePromptContext`. No wire change needed; pure client.

### 7. First-class subagent tool with parent visibility
**Status in figaro:** the `subagents` skill and the `bossman` skill both describe orchestration via `figaro send -f -j &` and log-file monitoring. That works, but the parent aria has no idea a child exists, no aggregated status, no cost roll-up, and the child cannot address the parent structurally.

**Why it matters:** Claude Code's `Task` tool spawns a subagent whose lifecycle is a first-class UI element, whose usage rolls up, and whose `SubagentStop` hook fires. It's the difference between "I orchestrated a fleet" and "I remembered to `wait`."

**Sketch:**
- New tool `spawn` (`internal/tool/spawn.go`) with args `{prompt, loadout?, tools?, timeout?}`. It calls `angelus.create` + `figaro.qua` on the child socket and streams the child's `aria.AriaFrame`s back as its own `onOutput`, with a synthetic node type `NodeSubagent` in `internal/livedoc` so the transcript renders it collapsible (mirrors `NodeSteering`).
- Parent-child link recorded in the child's `AriaMeta.ParentAriaID`; `figaro list` already has a tree view — just teach it this edge.
- Cost roll-up: parent's `refreshMetrics` sums child metrics via angelus. Cheap because metadata is already sidecar (invariant 15).
- Angelus grows a `angelus.subscribeChildren` notification so a parent can react to `SubagentStop`.

### 8. Per-project persistent memory (auto-loaded AGENTS.md)
**Status in figaro:** `agents.md` exists in *this* repo but nothing loads it. Users get project context only by writing a loadout or `figaro set system.credo`.

**Why it matters:** Claude Code walks upward from cwd for `CLAUDE.md` / `AGENTS.md`, includes them as system context, and `/memory` writes to the nearest one. It's the reason `cd` into a project "just works."

**Sketch:** On first turn of a fresh aria (the same boot-patch point that stamps `system.cwd`), walk `system.cwd` upward for `AGENTS.md` / `figaro.md` and inject each as a `<system-reminder name="project.<basename>">` chalkboard key. Bounded (≤ N files, ≤ M bytes). Add a `figaro remember "…"` verb that appends a bullet to the nearest such file — small, pure client. No wire change.

### 9. Named observability of the inbox / turn state via CLI
**Status in figaro:** `figaro status` shows mantra/provider/tokens; it does not tell you "turn active, 2 prompts queued, dispatching bash." `status.State` is only "active"/"idle" (`agent.go:468`).

**Why it matters:** For fleets and for scripts, "is this aria mid-tool-call or mid-thinking or waiting on a queued steer?" is what you actually want.

**Sketch:** Extend `figaro.Info` with `Phase ∈ {idle,thinking,tool,waiting_approval}` and `Queued int`; surface in `status` and `list`. Free once #3 lands — the fields already exist in the actor's state.

---

## MINOR / ALREADY-COVERED

- **Interrupt semantics.** Claude Code has *soft* (interrupt+steer) vs *hard* (kill). Figaro's Ctrl-C in `send` is soft-ish (sends `figaro.interrupt`, then a follow-up steer is a fresh `figaro --`). Fine; a `Ctrl-C twice` idiom (interrupt-then-compose-in-place) is a client polish item, not a protocol gap.
- **Session resume after daemon restart.** Handled: `RestoreBindings` + `restoreByID` (`internal/angelus/protocol.go:87`, `bindings.go:66`). Good enough for daily use.
- **Transcript navigation.** `internal/cli/transcript.go` is 1000 lines of well-thought pager (search, help panel, status panel). This is a strength, not a gap.
- **MCP servers.** Deliberately absent per invariant #11 ("One static binary"). Not a bug.

---

## Top 3 to build first

1. **Tool permission / approval modes** — safety floor; nothing else in the list is meaningful until `bash` and `edit` are gate-able.
2. **Lifecycle hooks** — the mechanism that makes #1, #4, #5, and half of enterprise adoption trivial. Ships once, unlocks four other gaps.
3. **Queued-message visibility** — smallest surface (a notification + a render row), largest daily-quality improvement. The plumbing is already there in the inbox; only the wire and painter need work.

*Nothing else edited, nothing else touched. À votre service.*