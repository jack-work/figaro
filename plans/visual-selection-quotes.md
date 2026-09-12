# Visual selection and quoted coordinates

> **STATUS: SPEC, nothing built.** Written 2026-09-11 from Gluck's brief. The
> estimate is at the end; the risk is all in §6.

## 1. What it is for

An agent writes eight hundred lines. The reader has four questions about four
different parts of it, and wants each answered in its own fork, without
retyping the passage each question is about.

```
v / V          select the passage
:              opens the command line ALREADY holding the range
:<,>fork -S mantra="the retry loop" -- what happens if this races?
```

The selection is not pasted. It becomes a **coordinate**, the daemon resolves
the coordinate against its own log, and what the agent receives is a short
quote it can recognise plus the reader's question. The agent already holds the
full text in its context: sending it back would spend tokens to tell it
something it knows.

## 2. The coordinate

### 2.1 The wire form

One token, at the **start of the message text**, in angle brackets:

```
<412.0:23-1180>                 one block of one message
<412.0:23-419.2:88>             a span across messages
<412.0>                         a whole block
<412>                           a whole message
```

`lt` is the figaro LT: the model's own coordinate, the one `fig show` prints
and the one a fork cuts at. `block` is the index into that message's content
blocks, and it is not optional dressing: a message that says a paragraph and
then calls a tool is one LT and two blocks, so an offset without a block is
ambiguous the moment anything but prose is involved. It defaults to 0 so the
short spellings stay short.

The offsets are **rune indices into that block's text**, half open, `[start,
end)`. They are rune indices and not bytes because the reader selected
characters on a screen, and because a byte offset that lands mid-rune is a
class of bug we can refuse to have.

The offsets address the SOURCE, not the render. `livedoc.Node.Markdown` is
`strings.TrimRight(content.Text, "\n")` and the node carries
`Src: [{LT, Block}]`, so a node's text and its message's block text are the
same string. That identity is what makes the coordinate resolvable on a server
that has never seen a terminal.

### 2.2 The shorthand

Inside the transcript, with a selection up, `:` opens the command line holding

```
:<,>
```

exactly as vim opens `:'<,'>`. It is a **placeholder**, expanded by the client
at submit time into the fully qualified form above. Outside the transcript, or
with no selection, the fully qualified form is typed verbatim and means the
same thing. One grammar, two ways to produce it.

### 2.3 Where it may appear

In command mode the range precedes the verb, because that is where vim puts it
and where the reader's hand goes:

```
:<,>fork -S mantra="the retry loop" -- what happens if this races?
:<,>send -- say more about this
```

On the wire it precedes the **prompt text**, because that is the thing it
qualifies:

```
fig send -- '<412.0:23-1180> what happens if this races?'
```

The client's command-mode parser is what moves it from the first position to
the second. Nothing else in the system has to know about two positions.

## 3. Resolution, and the refusal

`figaro.qua` is the one door every message goes through, and it already returns
its errors synchronously to the caller (`internal/figaro/server.go`, the
`MethodQua` arm). So:

1. Parse a leading coordinate token off `QuaRequest.Text`. No token: today's
   behaviour, byte for byte.
2. Resolve it against this aria's log. Every failure is a refusal, before
   anything is appended and before anything is queued:
   - no entry at that LT
   - the LT has no such block
   - the block carries no text (a tool invoke, an image)
   - offsets out of range, or start > end
   - a span whose two ends are in the wrong order
3. Extract, truncate (§4), and rewrite the message so what lands in the log is
   the quote plus the reader's text.

A refusal reaches the transcript through the path `:send` errors already take
(`commandAsync` → `noteErr` → the red status row), and a shell caller gets a
non-zero exit and the same sentence.

**The rewrite happens once, at the door.** The log holds the resolved message,
so a replay, a re-read and a fork all see the same thing the model saw, and the
coordinate does not have to be re-resolvable forever.

### 3.1 What else happens at the door today

The quote is not the first thing that inspects or rewrites a message on its
way in. Walking `fig send` to the inbox, in order:

| where | check | kind | sees | on failure |
|---|---|---|---|---|
| client `extractSendFlags` | `-S` is a JSON object or `k=v`; a bare name is refused | refuse | argv | die |
| client `atref.go` | `@key!` expanded from the aria's form | **rewrite** | form snapshot | **silent**: left literal |
| hub `authz.Guard` | caller identity from the envelope | annotate | params | anonymous |
| hub `authz.Rules` | `no-self-fork-during-turn` (the only rule) | refuse | method, params, identity, turn-active | Deny prose |
| hub `dressParams` | outfit names → patch; unknown outfit refused | **rewrite** | config, outfit dir | error |
| hub `route` | form-not-figaro, dormant, wake | refuse | hub kind | error |
| agent `MethodQua` | unmarshal; sender from the envelope | annotate | params | error |
| agent `combineFormInput` | `system.*` stripped from the client's context | **rewrite** | form snapshot | **silent** drop |
| agent `ApplyForm` → `CheckWritable` | system-managed keys refused to an unprivileged write | refuse | patch, catalog | error |
| agent `inbox.Send` | accepted; `accepted` published | | | |

Three things fall out of the table.

**There are already two token rewrites and they disagree about failure.**
`@key!` is expanded on the CLIENT, permissively: a reference that does not
resolve is left in the text and nobody is told. The quote must refuse, and it
must do so on the server because only the server has the log. Two prompt
tokens, two sides of the wire, two failure policies is the kind of drift this
project keeps finding in its own suites. `@key!` should move to the same
server-side pass, keep its terminator, and gain the same refusal (or an
explicit permissive flag, decided once).

**The hub already has the declarative half.** `authz.Rules` is a named table
whose entries see the request and return Allow or Deny-with-prose, and the
docs say adding a rule is a one-line data change. It cannot host the quote
because it runs before the aria is resolved and sees no form and no log. But
the shape is right and should not be reinvented.

**The rewrites are hand-switched.** `dressParams` is three near-identical
arms over `set`, `qua` and `cast`, each unmarshalling, folding, remarshalling
(one of them remembering to carry the `sender` envelope, the other two not
needing to). A fourth rewrite adds a fourth arm.

### 3.2 The unification, and its limit

An **admission** is one named step that may annotate, rewrite, or refuse a
request, and declares what it needs:

```go
type Admission struct {
	Name  string
	Needs Needs            // request | config | form | log
	Apply func(ctx, *Envelope) error   // rewrite in place, or refuse with prose
}
```

Two tables, because there are two places with different knowledge:

- **hub-stage**, per method, needing at most config: identity, the authz
  rules as they are, dressing. This is `authz.Rules` widened from "may refuse"
  to "may also rewrite", which is the smallest change that absorbs
  `dressParams`.
- **agent-stage**, needing the aria's form or log: prompt tokens (`@key!`, the
  quote), the context strip, `CheckWritable`. Runs inside `MethodQua` before
  `SubmitPromptFrom`, in table order, first refusal wins.

The order is the table, the prose stays in the rule, and a test can walk the
table and assert that every admission has a refusal case. That is the whole
of what "declarative" buys here; the request types stay typed and nothing
becomes a DSL.

The limit worth stating: the two silent rewrites should become LOUD before they
become entries in a table. Putting a silent drop into a nice table makes it a
documented silent drop.

Recommendation: build the agent-stage table as part of phase 2 (the quote is
its first entry, `@key!` its second), and leave the hub-stage widening for the
day a second rewrite shows up there. Do not touch `authz.Rules` for this.

## 4. What the agent receives

```
> quoting aria 4d6f8806 · turn 19 · lt 412.0 · chars 23–1180 (1157 chars)
> The retry loop backs off exponentially, but the jitter is applied before
> the cap rather than after, so a long tail of clients …
> … which is why the ninth retry is the one that stampedes.

what happens if this races?
```

Head and tail, joined by an ellipsis, because the head says which passage this
is and the tail says where it ended, and the middle is the part the agent can
reconstruct from its own context. Head is longer than tail, as asked.

### 4.1 Config

A new `[cli.quote]` section. The block below is what gets appended to
`~/.config/figaro/config.toml`, in the house style: every value IS the in-binary
default, so the file is the documentation.

```toml
# A quoted selection: `v`/`V` in the transcript, then `:<,>send -- …`.
# The agent already has the full text in its context, so what travels is a
# coordinate plus enough of the passage to recognise it. Every value here IS
# the in-binary default; change one to tune, delete a line to float with
# releases.
[cli.quote]
head_chars = 480      # runes kept from the START of the selection
tail_chars = 160      # runes kept from the END; head is the longer half
ellipsis   = "…"      # what stands between them
gutter     = "> "     # prefix on every quoted line; "" for none
header     = true     # the "quoting aria … lt 412.0 · chars 23–1180" line
```

A selection shorter than `head_chars + tail_chars + 1` is sent whole, with no
ellipsis: truncating 300 characters to 640 characters is a bug that reads as a
feature.

Two notes on where this lives. `[cli]` is the client's own section and
everything in it is client-side today, but this truncation happens on the
**server**, because the server is what owns the log and does the extraction. So
either the section moves to a server-side `[quote]`, or the client sends its
truncation policy with the request. I recommend the former: `[quote]` at top
level, beside `[memory]` and `[wire]`, which are also the daemon's.

## 5. The TUI

### 5.1 Modes

`v` and `V` are free bytes in `modeTranscript` (the keymap's taken letters are
`q s m y j k d u G g / n N : ? ! Q S T H X x`). Two new rows in the table, one
new pager sub-mode:

| key | mode | means |
|---|---|---|
| `v` | visual | character-wise, anchored where the cursor is |
| `V` | visual-line | whole rendered lines |
| `Esc` | | drop it, as it drops a node selection today |
| `y` | | yank the selected text (the existing copier, narrowed) |
| `:` | | open the command line pre-loaded with `<,>` |

`Ctrl-V` (column) is **out of scope**, at Gluck's instruction.

Motions inside visual mode are the pager's existing ones (`j k d u G g`, the
arrow cluster), plus a cursor within the row for `v`. Node selection (`^N`/`^P`)
stays exactly as it is: it is a different gesture with a different granularity,
and the two can coexist because they cannot both be active.

### 5.2 Painting

The selection bar already paints over glamour's margin at decoration time
rather than being baked into the cached row (`transcript_selection.go`), and
`sgr.go` already merges renditions per row. Character-level highlight is the
same machinery with a column range instead of a whole row. The existing
`transcript_sgr_paint_test.go` and the fuzz suites around the gutter are the
tests that will fail first if the column arithmetic is wrong, which is the
right place for that to be discovered.

## 6. THE CRUX: rendered rows have no source offsets

This is the whole risk, and it should decide whether the feature is phased or
deferred.

A node's rows come from `render.Prose` → **glamour**, which reflows, wraps,
indents, styles, turns `**bold**` into bold, `[a](b)` into styled text, and
inserts bullets and rules that exist in no source. `render.Row` carries
`{Text, Block, Mark}` and no provenance at all. So there is nothing today that
turns "the reader highlighted from screen row 14 column 6 to row 19 column 40"
into `<412.0:23-1180>`, and glamour cannot be asked for one.

Three ways out, in the order I would try them:

**A. A sequential rune matcher, computed lazily per node.** Walk the node's
source and its rendered rows together, rune by rune, skipping whitespace runs
that do not align and runes the renderer inserted; record, for each rendered
rune, the source index it matched. Build it once when visual mode first touches
a node, cache it beside the row cache, drop it when the row cache drops.

Accurate for prose, near exact for fenced code, and it degrades in a way we can
SEE: when alignment is lost for a row, that row's range is unknown, the
selection widens to the nearest known boundary, and the status row says it
widened. Roughly 1–2 days including a corpus of real agent output (tables,
nested lists, links, CJK, emoji, code fences) as the test.

**B. Render with provenance.** Replace glamour for the blocks we want to quote,
or wrap it so figaro does its own wrapping over a source-annotated token
stream. Correct by construction, and a rewrite of the prose renderer. Weeks,
and it touches every painted test in the tree.

**C. Coarsen the coordinate.** Offsets snap to **source line** boundaries: `V`
selects whole source lines and `v` is dropped for now. This needs only a
row→source-LINE map, which is far easier to keep aligned than a rune map
(glamour preserves line structure much better than it preserves columns), and
it delivers the actual use case: quoting a passage, not a phrase.

**My recommendation: ship C, then A.** C is a day of work and covers "I have
four questions about four parts of this answer". A is where character precision
comes from, and it can be added under the same coordinate grammar without any
wire change, because `<lt.block:start-end>` already says runes.

### 6.1 Two traps in the same area

**Tool output is clamped.** A tool node draws a tail-bounded window of its
output (`bashCap` lines), so the rows on screen are not the whole source. Any
mapping must anchor from the tail, and a selection that reaches the top row of
a clamped tool must not silently claim to start at the block's character 0.

**The inquiry is text on the turn.** It renders as a node with the sentinel
index `-1` and carries no `Src`, so quoting the reader's own question needs the
turn's opening input LT looked up separately. Small, but it is exactly the sort
of gap that gets found in a demo.

## 7. Fork

`figaro fork … -- <prompt>` forks first and then sends the prompt to the
branch, as an ordinary `qua`. So a quote in a fork prompt is resolved **against
the child**, which is right: the child inherits its parent's entries and their
LTs up to the cut, so the coordinate means the same passage.

The failure that follows from that: **if the cut lands before the quoted
region, the child does not contain it**, the qua is refused, and a branch has
already been created with nothing in it. The client knows both numbers, so it
should refuse before forking:

> `fork: the region you quoted (lt 412) is after the cut (lt 400), so the
> branch would not contain it`

`:fork` does not exist in command mode yet. `plans/transcript-command-mode.md`
§6 phases it after "verbs return lines", which is the refactor that stops the
pager from having a second dialect of the CLI. Two choices: wait for that, or
hand-write a `:fork` twin now and delete it later. Gluck's use case is the
fork, so this dependency is on the critical path and worth deciding early.

## 8. Phases and estimate

Gluck's correction, recorded: the work is done by an agent, with Gluck in the
design loop, and the first cut is **about half a day of focused effort**. The
phases below are the order and the done-conditions; they are not a schedule.
The estimate that matters is where the effort concentrates, which is §6 and
the pty case, not the volume of code.

| # | phase | done when |
|---|---|---|
| 1 | **grammar**: parse/format `<lt.block:start-end[,…]>`, pure, both spellings | a table test covers every malformed form and names what it refuses |
| 2 | **admission at the door** (§3.2): the quote resolved, truncated, rewritten; every refusal | a message with a bad coordinate never reaches the log, and says why |
| 3 | **config**: `[quote]`, defaults appended to config.toml | the short-selection case does not ellipsize |
| 4 | **visual mode**: `v`/`V`, painting, `Esc`, `y`, the pager's motions | a pty case selects, yanks, and the highlight survives a resize |
| 5 | **row→source map** (option C: lines first) | `V` over a wrapped paragraph produces the right source range |
| 6 | **`:` pre-loads `<,>`** and expands at submit | `:<,>send -- x` reaches the daemon as `<412.0:…> x` |
| 7 | **fork**: the pre-flight cut check; `:fork` as a hand-written twin | the stray-branch case is refused before the fork |
| 8 | **pty smoke**: select → `:<,>send` → the quote appears in the transcript | it fails when the coordinate is wrong, and says so on screen |

Phases 1–3 are server-side and pure. The moment they exist the feature works
from a shell with no TUI at all, which is the cheapest way to find out the
grammar is wrong before anything paints. Design decisions Gluck holds: the
grammar (§2), the quote's shape (§4, §9.2), option C-then-A (§6), and where
`@key!` goes (§3.1).

## 9. Open questions

1. **`[quote]` or `[cli.quote]`?** The truncation runs on the server (§4.1). I
   lean `[quote]`, top level.
2. **Is the quote plain text, or its own content block?** Phase 1 makes it text
   in the user message. A `ContentQuote` block carrying the coordinate and the
   excerpt would let the projector render it as a quote node and let a later
   reader click through to the original. Cleaner, and a wire change.
3. **Does a quote survive into the provider's view verbatim?** As text, yes, it
   is just part of the user message. As a block (2), the provider projection
   has to decide.
4. **Should `y` in visual mode copy the TEXT or the COORDINATE?** Text is the
   obvious answer; the coordinate is the useful one when you are about to type
   a shell command. Perhaps `y` text, `Y` coordinate.
5. **What happens to a selection when the transcript's subject changes?**
   `retarget` drops the node selection today; a visual selection must go the
   same way, and the `<,>` in a half-typed command line then refers to nothing.
   Refuse at expansion time with "the selection was dropped when the subject
   changed".
6. **Multi-node spans across a tool call.** A selection that starts in prose,
   crosses a tool widget and ends in more prose is one screen region and three
   source blocks. Does the quote carry all three (with the tool's output
   truncated separately), or does it refuse? I lean: carry the endpoints, quote
   head and tail, and say in the header that it spans N blocks.
