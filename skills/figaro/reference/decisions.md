# Decisions: `figaro decide`

Read this when you want a fast yes/no or multiple-choice answer instead of
prose, when you are choosing between form entries (`?? @skills`), or when you
are changing the `??` grammar, `internal/decide`, or the pager's decision pit.

A **decision model** takes context and one typed question and returns
calibrated probabilities. Two wires are spoken, and everything below holds for
both:

| API | Models | Endpoint | Context | Yes/no | Choice answer |
|---|---|---|---|---|---|
| `systemone` | jev, Microsoft decision-1 | `POST /systemone` | `state`, JSON, 32k tokens | `noul`, a probability | `probabilities` map, `confidence` |
| `openai` | gpt-6-luna | `POST /decisions` | `input`, text, ~1M tokens | `predicate`, a probability | `probabilities` array, `confidence` |

A yes/no is the same quantity on both, the probability the answer is yes, so
nothing is lost converting between them; an OpenAI predicate simply carries no
confidence. jev does not converse, so it is never an aria's `system.model`
(setting it there is refused). luna does converse, through the Responses API,
so it is not refused; as a decision model it is asked with the same verb.

## The verb

```sh
figaro decide -- is the retry loop idempotent?
figaro decide -C=ship,revert,wait -- which way?
figaro decide -C="option 1","option 2" -- which?
figaro decide -C @skills -- which skill fits this task?
figaro decide -- '<412.0:23-1180>! does the step grow with the slope?'
figaro -A decide --context - -- does this log show a crash? < run.log
```

| Flag | Means |
|---|---|
| `-C`, `--choices` | options, comma separated; quotes keep a comma; repeatable |
| `--id <id>` | read that aria instead of the one this shell is attended to |
| `-m`, `--model` | the decision model for this call |
| `--context <text>` / `-` | text to decide over (stdin with `-`) |
| `--budget <n>` | cap the context tokens sent |
| `-j` | the response as JSON |
| `-A` (global) | ignore the binding: decide over `--context` and training alone |

**It only reads.** Bound to an aria, `decide` reads its conversation and form
straight from the store: it never enters the aria's inbox, never waits on a
running turn, and writes nothing to the log. The answer is printed and that is
all. A figaro calling it from bash sees the answer as that tool's output, which
is how it lands in an LLM's context. The SDK has the same call:
`sdk.Angelus.Decide(ctx, rpc.DecideRequest{...})`.

## Options

Options come from `-C` and from a question whose last line starts with `??`
(space separated there, quotes respected). The two lists are merged in order
and a repeat collapses.

| Options | The question sent |
|---|---|
| none, or exactly `yes`/`no` | one probability that the answer is yes |
| two or more | a choice: every option scored, one preferred |
| one | refused: one option is not a choice |

`@key` names a form key on the aria:

- **A key with children** becomes one option per child, and the child's value,
  as JSON, is what the model reads about it. `@skills` makes every skill an
  option carrying `{"filePath": ..., "frontmatter": ...}`. One level only.
- **A leaf** is one option whose text is its string value.

Only the keys the options name are sent. Nothing else on the form reaches the
model, which is what lets an LLM fill a key (`figaro set candidates.a ...`)
for a later `decide -C @candidates` to choose between.

Order is kept as given, because the model leans slightly toward the first
option. Reorder once and ask again to check an answer that matters.

## What the model is shown

Budgeted in this order, against the API's limit for context plus question
(32k for System One; about 1M for OpenAI, measured 2026-10-14: 763k accepted,
1.05M refused), with a tenth held back because the count is an estimate:

1. **The question and its options**, whole. If they do not fit, the call is
   refused with the size, never clipped.
2. **The focus**: a leading quote coordinate's passage, whole, up to half of
   what is left.
3. **`--context`**, if given.
4. **The conversation** takes the rest: the newest messages, or, with a focus,
   a window centred on it and filled outward from both sides, spilling to one
   side when the other runs out.

Thinking and ceremonial messages are skipped; tool calls and results are
included, each clipped to a third of the budget. A smaller `--budget` is often
better: jev's accuracy falls as the state fills with unrelated text, and on
OpenAI a full window is a large bill ($0.10 per million input tokens).

System One is sent the state as JSON (`focus`, `context`, `conversation`);
OpenAI is sent the same parts as labelled text, since `input` is text.

## In the pager

| Gesture | Does |
|---|---|
| `>` draft ending in `?? a b c` | decides instead of sending |
| `>` draft ending in a bare `??` | opens the options line: comma separated, `^S`/Enter decides, empty is yes/no, `Esc` back to the draft, `^C` spends both |
| `v`/`V`, then `>` | the draft starts with `<,>`: the highlight becomes the focus |
| `:decide -C=a,b -- <question>` | the same, from the command line; `:<,>decide` for the highlight |
| `D`, or `:decide` alone | reopen the last decision; `D` again closes it |

The answer opens in the decision pit (`⁇`) with the focus and the preferred
option selected. Every option is a row: `j`/`k` move, `y` copies the one under
the cursor, `T` hands the keys to the conversation and back. The pit holds one decision, in this pager's memory only, and
the next one replaces it. Nothing is written anywhere, so it works the same
whether or not a turn is running. A `??` line meant literally is `\??`.

## Where the model comes from

`-m`, else the aria's `system.decide.model`, else `model` in
`providers/decisions.toml`, else `jev-latest`. The file also holds `base_url`,
`api_key`, `provider` and `api`; `DECISIONS_BASE_URL` overrides the URL. All
of it is read per call.

The API is `--api`, else the aria's `system.decide.api`, else `api` in the
file, else the model's name: `gpt-*` and `*luna*` speak `openai`, everything
else `systemone`. Name it outright where a model's name does not say.

`provider = "<name>"` borrows that provider's whole credential chain instead
of the keys below: its env var, its stored key, and the OAuth token hush
holds for it. It is how a host that takes no key is reached.

| Host | base_url | Credential |
|---|---|---|
| TypeSafe | `https://api.typesafe.ai/v1` (default) | `TYPESAFE_API_KEY` |
| Experiential Labs | `https://api.experientiallabs.ai/v1` | `EXPLABS_API_KEY` |
| a broker holding the key | `http://127.0.0.1:8787/v1` | none; model `explabs/jev-latest` |
| an OAuth gateway declared in `providers/<name>.toml` | its `base_url` | `provider = "<name>"` (`figaro login <name>`) |
| OpenAI luna through a broker | `http://127.0.0.1:8787/v1` | none; model `explabs/gpt-6-luna-decisions` (rou2's `openai/gpt-6-luna` on spain) |
| OpenAI direct | `https://api.openai.com/v1` | `api_key`, or `provider` naming one that holds an OpenAI key; model `gpt-6-luna` |
| Microsoft `decision-1` on Azure Foundry | `https://<resource>.cognitiveservices.azure.com/providers/microsoft/v1` | `provider = "azure"` (Entra, `figaro login azure`); model is the deployment name |

The System One body is closed on the far side: `model`, `state`,
`questions`, nothing else, and a broker that stamps request bodies has to
leave that path alone. OpenAI's endpoint accepted rou2's stamped
`safety_identifier` and `user`.

## Over `figaro serve`

`figaro serve` relays the same wire to anything on loopback that wants it,
with figaro's credentials:

| Request | Goes to |
|---|---|
| `POST /v1/systemone`, model `decisions/jev-latest` | the decisions host above, prefix removed |
| `POST /v1/systemone`, model `gateway/explabs/jev-latest` | the gateway provider's base URL + `/systemone` |
| `POST /v1/decisions`, model `<provider>/gpt-6-luna` | that provider's base URL + `/decisions` (OpenAI's wire) |
| `POST /u/decisions/systemone` | the decisions host, body untouched |

The relay rewrites the model and nothing else, so a closed body stays closed. The
decisions host is served when it has a key, or keyless when its `base_url` is
a broker. Point `figaro decide` at a serve with
`DECISIONS_BASE_URL=http://127.0.0.1:8788/u/decisions`.
