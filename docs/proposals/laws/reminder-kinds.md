# Reminder kinds

Status: proposal, revision 1, 2026-11-18. Nothing is built. Written by the
reminder-kinds lane (figaro `66ec0947`, a fork of the Figaro Engineer) for
Gluck, as a companion to [the laws proposal](README.md), Part 1, "How a law
reaches the model". Code facts are read at `28424a893` on
`lane/reminder-escape` (pheasant-goldfish, #143), which is `adccceea1`
(v0.74.1) plus the reserved-tag escaping. That branch is not on keel yet, so
paths below are repo-relative with line numbers at that commit.

**Are we grouping reminders?** No, with one exception: Anthropic, Gemini and
Responses give each reminder its own block, part or item, while the OpenAI
chat projection joins sender tag, prose and every reminder into one string
with blank lines; and in a turn that holds only tool results, Gemini, chat
and Responses fold the reminders into the last tool output's text.

## Names used here

| name | what it is |
|---|---|
| **reminder** | Text the daemon writes into a user turn that no person typed, wrapped by `provider.Reminder` as `<system-reminder name="...">`. |
| **kind** | Which family a reminder belongs to: `key`, `study`, `law`, `life` or `sender`. Today the kind exists only in which Go function built the text. This proposal puts it on the wire. |
| **flag** | The attribute the renderer writes to mark a kind, for example `kind="study"`. Fixed by the daemon, never configurable, never prose. |
| **say** | Optional prose a figaro or Gluck attaches to a kind, the way `system.fork_incantation` attaches "HUZZAH" to a fork today. Off by default. |
| **one reminder list** | A single provider-neutral function that returns a turn's reminders in canonical order, which every projection consumes instead of assembling its own. |

## Part 1: how reminders reach the wire today

### What every projection shares

- **The wrapper.** `internal/provider/reminder.go:17` `Reminder(name, body)`
  writes `<system-reminder name="NAME">\nBODY\n</system-reminder>`. The name
  is attribute-escaped (`reminder.go:22`); the body goes through
  `NeutraliseReservedTags` (`reminder.go:10`), which rewrites the `<` of
  `system-reminder`, `law`, `laws` in any case and spacing to `&lt;`.
- **Key reminders** (bound-form patches) come from `form.Render`
  (`api/form/render.go:71`): one entry per changed key, keys sorted
  (`api/form/form.go:561`), `system.*` keys skipped (`render.go:81`), a
  per-key template if the outfit has one, else the raw value. `datetime` is
  just a key (`api/form/known_keys.go:82`, mode ephemeral per turn) that the
  CLI stamps on each prompt (`internal/cli/config.go:152`), so it sorts among
  the others: `aria_id`, `datetime`, `mantra`.
- **Study reminders** come from `StudyReminderTexts`
  (`internal/provider/study_render.go:14`): a `study` reminder for a begin or
  drop mark, then one `study:<form id>` per changed studied form, ids sorted.
  Each body is JSON; the incantation's `say` rides inside it.
- **The fork reminder** comes from `ForkReminderTexts`
  (`internal/provider/fork_render.go:13`): only on the message carrying
  `system.forked_from`, and only if `system.fork_incantation` is set on the
  pre-patch bound form (`api/form/incantation.go:78`). No incantation, no
  reminder: a fork is told nothing.
- **The sender tag** comes from `SenderReminder`
  (`internal/provider/sender.go:10`): only for prose from another figaro, and
  it sits immediately before that prose. Gluck's prompts carry none.

So within one input message the order everywhere is: content in IR order
(each figaro's prose preceded by its sender tag, tool results, images), then
key reminders, then study reminders, then the fork reminder. Responses is
the exception below.

### Anthropic (`internal/provider/anthropic/anthropic.go`)

| fact | where |
|---|---|
| sender tag and prose are two `text` blocks | `:648-651` |
| key reminders, one `text` block each | `renderPatchBlocks`, `:745-755` |
| then study blocks, then the fork block | `:679-685` |
| `placeReminders`: if the message has any text or image, reminders are appended as top-level blocks after it; if it holds only tool results, they go **inside** the last `tool_result`'s content list as extra `text` blocks | `:1592-1620`, called at `:686` |
| `resultsFirst` then hoists every `tool_result` to the head of the user turn, so reminders end up after results | `:691`, `coalesce.go:22` |
| adjacent user rows are merged into one message, so a fork's birth row and the prompt that follows land in one user turn | `coalesceRowsSeq`, `rowseq.go:234`; `mergeRows`, `coalesce.go:59` |
| system blocks: the credo (with the OAuth "Claude Code" preamble as an extra first block) | `systemBlocks`, `:766` |
| breakpoints: last system block, last tool, last content block of the tail row; the fourth is left for a gateway or a per-LT tag from `system.tags` | `markCacheBreakpoints`, `:904`; `messageTags`, `:857`; `markRowsSeq`, `rowseq.go:267` |

The tail breakpoint lands on the last block of the last row, which in a
turn with reminders is the last reminder. Each reminder is its own block,
so nothing is grouped.

### Gemini (`internal/provider/gemini/`)

| fact | where |
|---|---|
| reminders are built as strings: keys, studies, fork | `gemini.go:434-436` |
| sender tag and prose are two text `Part`s | `wire.go:175-179` |
| `placeReminders`: with any text or image part, each reminder is its own text `Part` appended; with only `functionResponse` parts, all reminders are **joined with `\n\n`** and appended to the last response's `output` (or `error`) string, or put in a `reminders` field if neither exists | `wire.go:286-326` |
| then `resultsFirstParts` puts function responses first | `wire.go:183` |
| system: credo plus every `RoleSystem` row, all hoisted into `systemInstruction` | `pairAndCollectSystem`, `wire.go:357` |
| cache: none explicit; Gemini caches prefixes implicitly | `gemini.go:306` |

### OpenAI chat (`internal/provider/openaichat/`)

| fact | where |
|---|---|
| reminders built as strings: keys, studies, fork | `openaichat.go:313-315` |
| sender tag, prose and **every reminder joined into one string** with `\n\n` | `wire.go:151-154`, `:198-199`, `joinText` `:286` |
| that string becomes one `user` message: a bare string, or one text part when the route marks blocks | `textContent`, `wire.go:114` |
| in a results-only turn the joined reminders are appended to the last `tool` message's text | `wire.go:184-196` |
| cache: either a top-level `cache_control`, or one marker on the system message and one on the tail message's last part; `prompt_cache_key` when the route takes it | `markRequest`, `wire.go:356`; `openaichat.go:297-301` |

This is the one projection that groups: the model sees prose and reminders
as a single run of text.

### Responses (`internal/provider/responses/responses.go`)

| fact | where |
|---|---|
| sender tag and prose are two `input_text` parts in one user message | `:885-889` |
| key reminders are further `input_text` parts in that message | `:943-967` |
| `separateInputTextParts` prefixes `\n\n` to each adjacent `input_text` part | `input_boundaries.go:8` |
| results-only turn: key reminders are appended to the last `function_call_output` text | `:954-965` |
| **study and fork reminders are each their own user message item**, appended after everything else in the entry, even in a results-only turn | `encodeEntry`, `:1348-1362` |
| system: the credo as `instructions` | `:263` |
| cache: `prompt_cache_key` per figaro, one explicit breakpoint on the last assistant message | `:272`, `markPromptCacheBreakpoint` `:1304` |

The study/fork placement is an outlier: on Responses a studied form's delta
after a tool call arrives as a free-standing user message, which the other
three projections avoid on purpose ("a user message of their own reads as
an empty prompt").

## Part 2: kinds as a native distinction

### The kinds

| kind | what | today's name attribute | flags written by default |
|---|---|---|---|
| `key` | a change to a key on the figaro's own bound form | the key | `kind="key" name="mantra"` |
| `study` | a study begun or dropped, or a studied form's delta | `study`, `study:@id` | `kind="study" form="@id" event="began\|update\|drop" version="9"` |
| `law` | a law added, changed or repealed mid-life | (new) | its own tag, `<law>` / `<law-repealed>`, per the laws proposal |
| `life` | fork, succession, retirement, TTL warning, cast into a role | `fork` | `kind="life" event="fork" from="7f0fe854"` |
| `sender` | another figaro's name tag before its prose | `sender` | `kind="sender" from="7f0fe854"` |

Example, the delta Gluck wants told apart from a law:

```
<system-reminder name="study:@ee927149" kind="study" form="@ee927149" event="update" version="12">
{"changes":1,"set":{"handoff":"..."}}
</system-reminder>
<law name="role/@ee927149/report-to-role" source="role @ee927149" since="2026-11-18">
Report to @ee927149, never to Gluck directly.
</law>
```

**Why an attribute and not five tag names.** `name=` stays first, so every
existing grep for `<system-reminder name=` still matches. The escaper keeps
one reserved family (`system-reminder`, `law`) instead of growing a regex per
kind. Flag values come from a closed set in Go, never from data; `form` and
`from` are ids and go through the same attribute escaping as `name`. `law`
is the one kind with its own tag, because the laws proposal needs it
reserved and a reminder must never be able to look like one.

**Flags only, no prose, by default.** The renderer writes the attributes
above and the JSON body it writes today, minus `say`. Nothing like "you
began observing a form" is hard-coded in the harness. One change follows:
today a fork with no incantation is told nothing; with kinds, every fork
gets `kind="life" event="fork"` and the parent id. That is a flag, not
prose, and it is the native distinction Gluck asked for.

**Canonical order** (the one reminder list): `law`, then `life`, then the
content with its `sender` tags in place, then `key`, then `study`. Laws lead
because the laws proposal puts law changes at the head of the turn's
reminders. One function, `provider.TurnReminders(msg, bound)`, returns
`[]provider.Reminder{Kind, Name, Attrs, Body}` in that order; the four
assemblers above (`anthropic.go:676-686`, `gemini.go:434-436`,
`openaichat.go:313-315`, `responses.go:943` and `:1353`) stop building their
own lists.

### The hook: `system.say.<kind>`

Today two keys carry prose: `system.fork_incantation` (a string or
`{"onfork"}`) and `system.study_incantation` (`{"onstudy","onupdate","ondrop"}`),
both user-settable (`known_keys.go:48-49`) and read off the pre-patch bound
form so a cold retranslation reproduces the bytes. Proposed: one family of
plain string keys, read the same way, one per kind and event.

| key | replaces | who may set it |
|---|---|---|
| `system.say.fork` | `system.fork_incantation` | the figaro, Gluck, an outfit |
| `system.say.study.began`, `.update`, `.drop` | `system.study_incantation` | the figaro, Gluck, an outfit |
| `system.say.key` | nothing (per-key templates already exist in outfits) | the figaro, Gluck, an outfit |
| `system.say.sender` | nothing | the figaro, Gluck, an outfit |
| `system.say.succession`, `.retire`, `.ttl` | nothing yet | the figaro, Gluck, an outfit |
| `system.say.law` | nothing | owner-held only |

When set, the text rides as `"say"` in the body, exactly as now. The flags
cannot be overridden: a figaro may add words to a kind, never change which
kind a reminder is. `system.say.law` is owner-held because a say renders
inside the `<law>` element; a figaro-writable say there would be a figaro
writing text into a law, which the laws proposal forbids.

Flat string keys, one per event, instead of a JSON object: each is settable
with plain `figaro set`, diffs read cleanly in a study, and the owner-held
`law` entry does not need a special case inside a shared object.

### What "a different block" can mean per provider

| provider | what exists on the wire | proposed | cost |
|---|---|---|---|
| **Anthropic** | Every reminder is already its own `text` block. The other user-side block types are `image`, `document`, `search_result` and `tool_result`. A `document` block (plain-text source with `title` and `context`) is a real type distinction, but the model reads it as attached material rather than instruction, it costs wrapper tokens, and whether it may sit inside `tool_result` content needs checking against the API before anyone builds on it. The system array is the only channel no transcript text can reach. | Keep `text` blocks with flags. Laws go in the second system block, per the laws proposal. | About 6 to 12 tokens per reminder for the attributes. The flags change every reminder's bytes, so `Fingerprint` goes `anthropic/tag/v10` to `v11` (`anthropic.go:428`): each figaro's wire cache is cleared (`ClearStaleTranslationCache`, `catchup.go:366`), the transcript retranslates, and the next turn is one full cache write. Breakpoint count is unchanged. |
| **Gemini** | `Part`s carry no type or metadata beyond `thought`; text is text. `functionResponse.response` is a free JSON object. `systemInstruction` is separate. | Keep one text `Part` per reminder with flags. In a results-only turn, always put reminders in `response.reminders` (today only a fallback, `wire.go:324`) instead of appending to `output`, so the tool's output stays the tool's. Laws in `systemInstruction`. | Implicit caching only: a byte change in early rows misses once, with no write premium. Fingerprint `v2` to `v3` (`gemini.go:85`). A mid-life `RoleSystem` row is hoisted into `systemInstruction` (`wire.go:357`), changing the prefix: laws must not take that path except at refold. |
| **OpenAI chat** | Messages of role `system`/`developer`/`user`/`tool`; a user message's content may be a string or a part list. | When the route marks blocks, emit each reminder and each sender tag as its own text part instead of joining. In string mode, keep the join but with flags, which is all the separation that mode allows. Laws in the system message at index 0, refolded at cold. A mid-conversation `system` message is not proposed: routes that front Anthropic hoist it into the system prompt, so every law change would drop the cache. | Parts add a few tokens each. Fingerprint `.../v6` to `v7` (`openaichat.go:84`), one full write per figaro. |
| **Responses** | Input items; a `message` item may have role `developer`, which OpenAI's chain of command ranks above `user`. | Laws as a `developer` message item: the one provider where a different block also carries authority natively. Other kinds stay `input_text` parts with flags, and study and fork move into the same user message as the key reminders (or into the last `function_call_output` in a results-only turn), ending the outlier. | One item header per law change. Fingerprint `/v7/` to `/v8/` (`responses.go:1090`), one full write per figaro. Whether Copilot's Responses proxy accepts `developer` items mid-input is unverified. |

Ship the flags and the per-provider moves in the same release so each
figaro pays one cold write, not two.

### Laws stay unforgeable

`lane/reminder-escape` already reserves `law` and `laws` in
`NeutraliseReservedTags` (`reminder.go:8`), so a body carrying `<law>`
renders as `&lt;law>`. Kinds add nothing to reserve: `kind=` lives inside a
tag that is already reserved, and a body cannot open that tag. Two gaps
remain from the laws proposal and are not closed here:

- tool results are not escaped yet (the #143 commit says so); the laws
  guarantee needs them escaped too;
- `law-repealed` is reserved by `\b` after `law`, which matches; the test
  should say so explicitly.

Any code from this proposal bases on `lane/reminder-escape`, not main, since
`provider.Reminder` is the one place the flags get written.

## Part 3: grants for studying other figaros

Today (study-bound, 0.59): a figaro may study another figaro's bound form
only if the caller holds `read` on `aria:<id>` **and** is the owner
(`internal/angelus/sandbox_grant.go:172-180`). The `--keys` filter is chosen
by the studier and bounded by nothing. The reason it is owner-only
(`c0ab79988`): read is checked when the study is declared, not when each
delta is delivered, so revoking read later does not stop the flow.

Grants since 0.60/0.61 live in `system.scopes`, a JSON array of
`verb:object` strings (`internal/authz/figaro_grants.go:31`), with objects
from the policy selectors (`aria:`, `lineage:`, `role:@`, `outfit:`), and
they only narrow down the lineage: a figaro grants only what it holds.

Proposed shape, rule left open:

1. **Deliver-time check first.** Store the studying principal with the
   study and re-check at each delivery. Without this no grant can open
   studies beyond the owner safely.
2. **A `study` verb.** `study:lineage:<id>` lets a figaro study the bound
   forms of a lineage; holding `read` no longer implies it.
3. **Which keys.** Two shapes for "which values of which figaros":
   - **A, in the grant:** `study:role:@ee927149#handoff,mantra`. One string
     says who and what. Needs a grammar change in jkauthz.
   - **B, beside the grant:** a protected `system.study.keys` map from
     selector to key prefixes, written by the same writers as
     `system.scopes`. No grammar change, two places to read.
   Either way the keys a studier sees are its `--keys` request, intersected
   with what its grants allow, minus restrictions, recomputed at delivery.
4. **Restrictions.** Key prefixes that no grant can open, set by Gluck:
   at least `law.` (laws never render as studies) and plausibly `system.`
   and `handoff`.

The exact rule waits for the end of Gluck's sentence ("control exactly what
values of which figaros we can ..."), relayed by the Engineer.

## Staged plan

| stage | what | breaks | reverse |
|---|---|---|---|
| 1. one reminder list | `provider.TurnReminders`; the four projections consume it. Golden tests pin today's bytes, so Responses' study/fork placement is kept for now | nothing | revert |
| 2. flags and per-provider moves | `kind=` and friends on every reminder; forks always get a `life` reminder; chat splits parts in block mode; Gemini moves results-only reminders to `response.reminders`; Responses moves study/fork into the turn's message | every figaro's provider cache drops once (fingerprints: anthropic v11, chat v7, gemini v3, responses v8); forks that were silent now see a flag; anything matching the exact old bytes (tests, skills quoting a reminder) must be updated | ship the old fingerprints back |
| 3. `system.say.*` | the hooks replace both incantation keys; `doctor normalize` rewrites `system.fork_incantation` and `system.study_incantation`; `figaro set` of the old keys refused with the new name | outfits and forms carrying the old keys until normalized; the HUZZAH incantation moves to `system.say.fork` | the old readers are small; restore them |
| 4. law kind | laws proposal stage 1 renders through the list; Responses laws as `developer` items | as in the laws proposal | as in the laws proposal |
| 5. study grants | deliver-time check, `study` verb, key bounds | studies held by a non-owner whose grants no longer cover them stop delivering | drop the verb; owner-only returns |

Stages 2 and 4 each cost one cold write per figaro; shipping them together
costs one.

## Open for Gluck

1. **Attribute or tag names?** Proposed `kind=` on `system-reminder`, with
   only laws getting their own tag. Distinct tags (`<study>`, `<life>`) read
   a little cleaner but each must be reserved and escaped.
2. **A fork with no incantation now gets a flag.** Proposed yes: that is the
   native distinction, and it is not prose. Or keep silence as the default?
3. **Laws on Responses as `developer` items?** The only provider with a
   native authority tier. Worth relying on, given the Copilot proxy is
   unverified?
4. **Should Gluck's own prompts carry a `sender` flag?** Today only other
   figaros get one, so "no tag" means Gluck. A flag would make that explicit
   at a few tokens per prompt.
5. **`datetime`, `aria_id`: `key` or a separate `turn` kind?** They are
   per-turn stamps, not state the figaro chose. A separate kind would let a
   figaro tell its board from the clock.
6. **The end of the sentence:** "configure grants to each of the figaros and
   restrictions such that we can control exactly what values of which
   figaros we can ..." What comes next decides Part 3 shape A or B.
