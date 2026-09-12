# Review of the quote pass

Reviewed commit: `5c290c83`, released in v0.36.2.
Reviewer: aria `27068b2c`, commissioned by `23e03e27`.
Main was read-only. No product changes, commits, installed-binary changes, or live-daemon tests.

## Gluck's preferences, from his own words

I read the user inquiries, not the other agents' descriptions of his preferences.

- `81bbb482`, turn 19: "remove all inline justification and excessive documentation" and "even the doc comments should stay slim". He also asks to clean neighboring code and remove dead code. This is directly applicable to the new file headers.
- `324a4f6d`, turn 8: "cut out the verbosity, agent can figure out most of that, and focus on simple happy path commands".
- `324a4f6d`, turn 11: "Try to centralize them in a single docskill directory"; "for incorrect, inconsistent or old docs, just remove them"; "figaro should be able to call the right command after reading only a couple pages". The same inquiry explicitly prohibits em dashes.
- `324a4f6d`, turn 14: "dont overthink it. but simplify the flow if possible. less automatically injected docs is best".
- `1799d04b`, turns 65-66: "what is this backward compatibility device? im not sure we need it" and "just do a flat migration ... rather than retain the backwards compatibility". This supports removing obsolete paths rather than maintaining parallel implementations.
- `23e03e27`, turn 36: "use the exact parser that we use over cli"; without `--stay`, fork should both listen and change terminal attendance; no single command should fork and listen without changing attendance. This is a behavioral requirement, not just naming preference.
- `23e03e27`, turn 30: "test explicitly against frame jitters and glitches" and "test extra hard for performance when selecting broad ranges and also allowing content to phase out of the tuis memory". The painting, reflow and eviction tests are requested work, not automatically excessive testing.
- `23e03e27`, turn 50: "strict stays" and "No inline commentary, elegant design philosophy, absence of excessive unit testing, keeping the skilldocs up to date and fresh especially with this forking behavior". This is the direct authority for this review. I did not find a separate earlier numeric test budget, so I have not invented one.

The repository's `agents.md` agrees on defaulting to no comments, not restating code, and not keeping compatibility shims. `skills/figaro/contributing/updating-docs.md` requires one authoritative location, short entry points and claims verified against source. Its maintaining guide requires meaningful canaries, not checks that can only agree.

## Verdict

The interface choices are mostly useful, but this needs a corrective pass. The biggest issues are correctness and incomplete ownership boundaries, not the number of files.

| Category | Grade | Assessment |
|---|---|---|
| Comment volume | D | 902 comment-only lines among 4,029 added production Go lines. Several new explanations already contradict the implementation. |
| Tests: volume and value | C | Useful renderer, reflow and refusal tests; also a tautological two-door test and smoke assertions that accept echoed prompts as answers. |
| Design | C | Good grammar package and delta-ref axis. Request-global dressing, duplicate tokenization, output-writing helpers, and separate navigation paths leave the refactor unfinished. |
| Skill documentation | F | The release does not teach quoting, visual selection or `:fork`; the main CLI guide still assigns verbosity to Ctrl-O. |
| Wire/config hygiene | C | Additive `Prev`, isolated grammar and top-level `[quote]` are sensible. Tool-source offsets are incomplete; quote extraction is not memory-bounded; zero budgets send everything. |
| Prose signature rule | A for added lines | No added line in the squash contains an em dash. The issue is excessive and stale explanation, not that character. |

## Findings, ordered by consequence

### 1. P1: form values can send terminal control sequences

Locations: `internal/cli/formdeltas.go:128-140,244-252`; `internal/livelog/render/lines.go:53-75`.

`unquote` now JSON-decodes strings. `flatten` only normalizes whitespace. A stored string containing `\u001b[2J` therefore becomes an erase-display sequence in the delta row. The later clipper explicitly preserves ANSI escapes. This is newly reachable through the changed table renderer; the previous representation printed escaped JSON.

Confirmed with the actual `renderTurnRows` composition path: a delta value `safe\x1b[2Jhidden` leaves that exact escape in the final row. This is not a claim based solely on a string helper.

Preference: correctness at the rendering boundary; the user's explicit request to test glitches.

Smallest fix: sanitize all untrusted key/value/banner text before applying table styling. Do not feed decoded terminal controls to a clipper that accepts styling escapes. Extend the existing delta-row test with one control-sequence case.

### 2. P1: live form-delta folding is nondeterministic

Location: `internal/formdelta/formdelta.go:284-311`.

`AttachOne` ranges over a map keyed by LT, while `merge` overwrites equal keys. Two updates to the same key can therefore be folded in reverse order. The dormant `Attach` path walks LTs in order; the newly added live path does not.

Nix probe: two records changing `a.mantra` to `old` and then `new`, attached 1,000 times, produced `old` 125 times. Identical durable records do not yield identical live deltas.

Preference: one deterministic projection, not two subtly different implementations.

Smallest fix: feed both paths ordered records or sort the LTs in `AttachOne`. Decide whether a collapsed transition retains the first `Prev` and last `Value`; currently overwriting also loses the initial old side. Add this case to the existing attachment tests rather than another test framework.

### 3. P1: quotes of long tool output resolve to the wrong text

Locations: `internal/cli/transcript_source.go:181-187`; upstream producer `internal/compose/compose.go:29-32,156-172`.

The client calculates the quote offset from `n.Output` as if it were the full tool-result block. It is not: the producer has already reduced it to the last 200 source lines. The client accounts for its own further display clamp but not the producer's removed prefix.

Confirmed through `compose.Nodes` and `nodeQuoteSource`: a 300-line result shows `L291`, emits offset 950, and that offset resolves to `L191` in the durable block. The producer removed 500 characters. The coordinate is valid, so the backend accepts and quotes the wrong passage without a warning.

Preference: precise source coordinates and honest refusal instead of a guessed range.

Smallest fix: carry the producer's source offset with the output preview, then add the client's display offset. Alternatively refuse such previews until that provenance exists. The current three-line tool fixture never crosses the producer cap; replace or extend it with this 300-line case.

### 4. P1: a prepared command does not own its form patch

Locations: `internal/cli/verbs.go:209-250`; `internal/cli/send.go:265-272`; `internal/cli/config.go:217-241`; `internal/cli/command.go:53-63,195`.

Parsing writes package-global `promptDressing`. Submission later reads that global through `buildPromptForm`, instead of the dressing held in the parsed plan. Command execution starts independent goroutines, so an outstanding send/fork can pick up another command's `-S`, `-D` or `-O`.

Deterministic RPC probe: parse a send with `mantra=first`, parse another with `mantra=second`, then submit the first plan through `sendVerb`. The received first question carries `mantra="second"`.

Preference: elegant ownership and a real shared verb body, not a global side channel left behind the new interface.

Smallest fix: make parsing side-effect-free and pass the plan's dressing explicitly to a prompt-form builder. Include the prepared-request interleaving in a request-level test. Serializing only the router does not fix it: overlay commands bypass `routerMu`.

### 5. P2: fork can still listen without changing attendance

Locations: `internal/cli/verbs.go:179-193`; `internal/cli/command.go:169-176,200-216`.

Example: the shell attends A, then `:listen B`, then `:fork -- question`. The command fills `plan.spec` with B. `forkVerb` refuses to rebind because B is not the shell's attended A, but `commandFork` still retargets to B's child. This is exactly the one-command fork-and-listen-without-attend case Gluck excluded in turn 36.

The shipped smoke test only forks the already-attended aria, so it cannot detect this distinction.

Smallest fix: make binding intent explicit in the shared operation. The TUI's default fork requests attend-and-show irrespective of its previous shell binding; `--stay` requests neither. Keep the shell's fan-out policy separate if that is still desired. Test from `listen B` while bound to A.

### 6. P2: the jumplist is wired to only some attendance paths

Locations: `internal/cli/command.go:275-309,334-348`; `internal/cli/command_router.go:208-211`.

`a` records the departure through `attendFromPager`. `:attend` calls `switchSubject` directly and records only the arrival. Startup does not seed the list. Thus the first `:attend B` from A leaves only B in the list, so Ctrl-O cannot return to A. `:fork` retargets directly and bypasses the attendance history entirely. A failed hop also advances the history cursor before the switch succeeds.

Preference: one navigation operation behind keyboard and command entry points.

Smallest fix: centralize successful attend transitions and record both source and destination there, including forks. Keep back/forward cursor changes transactional with the switch. Extend the existing jumplist test through the command path, not only the standalone list struct or `a` path.

### 7. P2: range-prefix expansion changes the user's prompt

Location: `internal/cli/transcript_jump.go:506-512`.

`expandRange` uses `strings.Fields` and joins the words before the shell-like tokenizer runs. It therefore collapses spaces even inside quoted arguments. Confirmed: `<412.0:0-4>!send -- "a  b"` sends `a b`, not `a  b`.

Preference: one parser; the remainder of the sent message must be preserved after the quote.

Smallest fix: parse once into argv, insert the coordinate into the prompt field, and dispatch that plan. If retaining the string boundary temporarily, use the same lexer with source spans rather than re-tokenizing with `Fields`.

### 8. P2: selection painting resets unrelated text styles

Location: `internal/cli/transcript_visual.go:489-498,535-537`.

Closing a wash/cursor emits a full SGR reset. It restores the wash in one case, but never restores the underlying foreground or attributes. For a red `abcdef`, washing columns 1-3 leaves the unselected `d` in default foreground rather than red.

Confirmed with the existing cell interpreter. The new fuzzer checks text, backgrounds and final rendition; it does not compare underlying foreground/attributes outside the selected cells, so this passes its stated assertions.

Preference: preserve what was already painted; test actual cell appearance rather than only the new effect.

Smallest fix: restore the underlying SGR state when leaving the overlay. Extend the existing cell oracle's expectations instead of adding another collection of exact escape-string goldens.

### 9. P2: the small quote still allocates the whole passage

Locations: `internal/figaro/input_quote.go:98-120,182-196,201-212`; `internal/figaro/agent.go:561`; `internal/cli/transcript_visual_bench_test.go:29-44,99-111`.

Spans gather every intervening text block and join them. Truncation subsequently converts the entire passage to `[]rune`. A rune slice also converts its whole input. The context exposed by `InputRewrite` is discarded at submission (`context.Background`) and ignored by quote extraction.

Nix, Go 1.26.1, three-iteration probe using a prebuilt MemLog and the real rewrite path, output under 1 KiB:

| Passage | Time/op | Bytes allocated/op |
|---|---:|---:|
| 1 KiB | 9.1 us | 8,032 |
| 4 MiB | 6.15 ms | 16,781,944 |

These are sizing observations, not statistical performance claims. They exclude the log storage itself. Across-message spans additionally allocate the joined text.

The reported frontend benchmark varies the number of *turns*, each with one small node. It neither exercises this server cost nor the long-single-turn case: `entriesOfTurn` binary-searches the turn, then several helpers scan that turn's rows.

Smallest fix: stream text through a bounded head/tail accumulator and a rune counter; avoid materializing the middle. Pass the request context and check it during large walks. Add one meaningful large-passage benchmark alongside a many-nodes-in-one-turn client case, not more tiny parser tests.

Related config edge: `head_chars=0, tail_chars=0` disables truncation and sends the entire selection. Probe: 10,000 source characters produced 10,004 output bytes. Define zero explicitly; negative values currently become zero silently. Do not let a zero budget accidentally mean unlimited.

### 10. P2: the verb boundary still has presentation and parsing leaks

Locations: `internal/cli/fork_wait.go:41-42,55-56`; `internal/cli/target.go:114`; `internal/cli/command_router.go:203-211`; `internal/cli/verbs.go:222-250`.

The shared helpers used by TUI verbs still write to global `stderrw`: a node-cut adjustment, the slow-fork notice and role redirection. Overlay commands do not run through `routeCaptured`, so these writes can reach the terminal while the pager owns it. This is the original failure the new verb boundary claims to remove.

The parser claim is also incomplete. `listen`/`attend` still join arguments into a string rather than run the CLI command parser. `:attend null` bypasses the shell wrapper's special unbind case. `sendVerb` accepts send's flags but only performs `Qua`: for example `-x` is accepted without shell execution semantics or an explicit refusal. A coordinate target is passed straight to Attach rather than the shell's send-at-coordinate flow.

Smallest fix: shared operations return progress/notes to their caller, and one typed plan explicitly represents the supported flags. A TUI-only restriction should be a declared capability refusal, never a parsed-and-ignored option. The useful separation is plan, operation, presentation; moving functions into `verbs.go` alone does not establish it.

### 11. P2: the bundled skill cannot teach this release

Locations: `skills/figaro/SKILL.md:86-108`; `skills/figaro/cli.md:49,165-179,294`; `skills/figaro/reference/trunks.md:73,128-153`; `skills/figaro/reference/ui-stream.md:381-405,445-473`.

Only `reference/ui-stream.md` changed under the shipped skill tree: 12 added lines, 6 removed. The new grammar and most usage instructions are in a 392-line `plans/visual-selection-quotes.md`, outside the bundled skill. That plan still describes unbuilt phases and old choices.

Docs-by-use exercise:

1. Followed the injected SKILL index to `cli.md` and `reference/trunks.md` for shell quoting. Neither documents the quote token, its `!` expansion rule, strict form references, or `[quote]` settings.
2. Tried to establish `:fork` attendance and `--stay` behavior through those pages. They document the shell verb only. No `:fork` or `:listen` walkthrough exists in the skill tree.
3. Followed the index to `reference/ui-stream.md` for keys. Release source documents M-m and the new jumplist, but the top-level CLI guide and trunks reference still direct a reader to Ctrl-O. The pager key table contains neither `v`/`V` nor `Y`, and describes `:` only as a coordinate jump.

The currently injected bundle (`e4931c4fb331f487`) predates the source update to M-m. I repeated the searches in the released source, so the finding does not depend on the installed daemon being old.

Smallest fix: put short executable examples and the current semantics in one bundled reference page, link it from the index/CLI page, and correct the old Ctrl-O references. Document the intentional limitations there: multi-sender inquiry, tool arguments, evicted selections, and prefix reloads. Do not expand SKILL.md into another specification.

### 12. P2: several tests certify less than their names claim

Locations: `internal/cli/command_listen_test.go:39-77`; `internal/cli/tmuxsmoke_cases_test.go:678-711,789-818`; `internal/cli/showdelta_test.go:12-32`.

- The fork two-door test literally calls `planFork(argv)` twice. Neither the CLI door nor the TUI door is exercised. It keeps passing if either door stops using the parser. The send test compares a wrapper with the function it wraps, not the submissions.
- The fork smoke test accepts `strings.Contains(vis, "FORKOK")`, although the displayed inquiry itself contains FORKOK. This can pass before an answer. The neighboring fork-navigation test already uses `bodyLines`; reuse that rule.
- The fork-banner placement assertion requires only `bannerRow <= answerRow`. A banner on the parent's turn is also before the child's answer, so that assertion alone cannot catch the reported placement bug. Check it lies after the child inquiry and before the child answer.
- The show test says `-v` opens the table but calls a renderer with `renderSettings{verbose:true}`. The CLI derives that field from `opts.details` (`-o`); `-v` takes the raw-IR path. This is another fixture not reaching its named door.

Preference: fewer tests that prove real properties. Replace these assertions with real entry-point/request comparisons and exact body/turn boundaries. Keep the canary for seam placement, the cell interpreter, fuzz corpus and actual reflow/eviction cases. A blanket deletion of unit tests would discard the best evidence in this change.

### 13. P3: explanations already contradict the code, and dead helpers shipped

Locations:

- `internal/cli/command.go:20-23` says sending to another aria follows it unless `-f`; `commandSend:100-103` correctly says it never switches.
- `internal/cli/keymap.go:298-304` says verbosity moved to plain `o`, while the actual binding at `keymap.go:196-206` is M-m. The new inventory also lists `o` as taken. The table was the requested inventory location, but it should be generated or derived from bindings rather than become a second definition.
- `internal/cli/verbs.go:3-23`, `internal/figaro/input_rewrite.go:3-20`, and `internal/cli/formdeltas.go:14-20` retell why the feature was commissioned. The useful contracts fit in a few lines.
- `internal/cli/transcript_source.go:442-457`: `quotePreview` has no callers.
- `internal/figaro/input_refs.go:123-131`: `snapshotOf` is used only by a test but is compiled into production.
- `internal/cli/transcript_visual.go:274-297`: `found` is initialized false and never set; the seed loop returns on its first valid point. Remove the unused tracking state.

Preference: slim documentation, no inline justification, clean neighboring code. Delete the stories and dead code; retain offset units, half-open bounds, source ownership and lock contracts. Tests/commit history already preserve the bug narratives.

## Additional issues worth fixing in those same files

- `formdeltas.go:149-181,128-130`: decoding `""` into the same empty Go string used for absence renders an existing empty string as `∅`. Confirmed by probe. Keep presence separate from display text; print an empty JSON string distinctly.
- `formdeltas.go:113-117` and `transcript_selection.go:92-97,501-505`: expanded values are still clipped to terminal width, and copying/hashing the table uses a fixed width of 200. Expansion does not guarantee access to full values; distinct tails past column 200 hash and copy identically. Use raw structured data for identity/copy, rendered rows only for display.
- `fork_quote.go:22-25,38-52` explicitly admits turn-cut preflight does not check the inquiry LT. It approximates the boundary from the first assistant node, although the actual fork cuts earlier. Head forks also do not validate quote existence, and a coordinate supplied by a form reference bypasses client preflight. If the promise is no orphan branch on bad input, validation and fork creation need a server-owned preparation operation, not this approximation.
- `projector.go:107-132` reads/materializes log and form history under `aria.Server`'s lock via a callback. The previous turn having no source nodes makes `from=0`, turning seal into a whole-history walk. Prefer the known prior boundary and compute outside the server lock, then stamp the resulting deltas.
- `transcript_source.go:138-143` treats zero-width runes as one cell. The source matcher/motions and painter therefore use different column arithmetic for combining characters. The fuzzer explicitly excludes these from its per-cell claim. This is an acknowledged test limit, not evidence that character mapping is complete.

## What to keep

- `api/quote` is dependency-light and gives client/server one coordinate grammar.
- Resolving before applying the form patch or enqueuing is the right refusal boundary. Strict `@key!` is approved; do not reverse that decision as cleanup.
- `InputView` cannot append. The two built-in rewrites are simpler than an extensible policy engine; there is no reason to add another DSL.
- A delta axis in `nodeRef` preserves ordering without inventing fake durable node IDs.
- Palette roles and the existing cell interpreter are appropriate foundations. Fix the missing restoration, not the whole renderer.
- Prefix reuse across aria switches is still deferred. I did not treat this agreed scope boundary as a regression. Avoid describing the existing cold reload as memory reuse.

## Verification and artifacts

All probes used `nix develop .#default` with config/runtime/state paths overridden to empty scratch directories. Go was 1.26.1. No provider calls or production daemon were used. The Go overlay adds temporary same-package tests without touching the checkout.

Artifacts are under `/var/tmp/23e03e27/probes/`:

- `overlay.json`, three `*_review_test.go` files: small exploratory probes, not committed tests.
- `results.log`: deterministic request interleaving, prompt whitespace, empty-value rendering, terminal-control bytes, randomized live-delta fold, zero quote budget, and allocation benchmark.
- `paint.log`: foreground preservation failure and the control escape surviving full turn composition.
- `tooltail.log`: the real producer-to-client tool offset failure (`L291` becomes `L191`).

The failing probe exit codes are intentional: they assert the requested behavior and demonstrate the released defects. I did not rerun the full existing suite or a real-provider pty session. Emitted-byte and cell-interpreter proofs establish the listed renderer defects; they are not being described as human screenshot observations.

The highest-value next pass is to correct the four P1 findings, finish request/navigation ownership, replace the vacuous checks, then trim code commentary and publish concise skill instructions. No additional broad test framework is needed.
