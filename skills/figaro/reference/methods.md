# Methods: form values a decision model derives

Read this when you want a form key set for you on every inquiry (the reasoning
effort, say), when a key changed and nobody ran `figaro set`, or when you are
changing `internal/methods`.

A **method** is a form entry under `system.methods`. On every inquiry, before
the provider is called, figaro renders the method's template, asks the aria's
decision model to pick one of its options, and writes the pick to the method's
key. The turn then runs on the new value.

```toml
[system.methods.reasoning-effort]
key     = "system.thinking_effort"
event   = "inquiry"
context = """
How much reasoning does the newest request need?

Earlier: {{inquiry~2}}
Earlier: {{inquiry~1}}
Newest:  {{inquiry}}
"""
options = ["low", "medium", "high", "xhigh"]
score   = true
default = "high"
```

| Field | Means | Default |
|---|---|---|
| `key` | the form key the pick is written to | required |
| `context` | the template; it is the whole of what the model reads | required |
| `options` | an array of names, or an object of name to description | required, two or more |
| `event` | when it runs; only `inquiry` exists | `inquiry` |
| `score` | read the options as a scale, lowest first (see below) | `false` |
| `default` | written when no answer arrives in time | none: the key is left alone |
| `keep_cache` | run only where changing `key` keeps the prompt cache (see below) | `true` |
| `enabled` | this method on or off | `true` |

`system.methods.enabled = false` turns every method off. Each switch takes a
boolean or the string `"false"`, so `figaro set system.methods.enabled false`
works either way.

## The template

`{{inquiry}}` is the prompt being sent. `{{inquiry~N}}` is the Nth inquiry
before it, counted back from the tail of the conversation; one that does not
exist renders empty. Steers do not count. Any other `{{...}}` refuses the
method.

The conversation is not sent: the rendered template is the question and the
whole of the context, so what the model sees is exactly what you wrote. The
text goes as written, so a `??` line or a quote coordinate inside an inquiry
is not the `figaro decide` grammar here.

## Choosing

Without `score` the model is asked a choice and its preferred option is
written. With `score`, System One (jev, decision-1) is asked its `score`
question and returns the probability-weighted level; luna has no score
question, so it is asked a choice and figaro weights the probabilities itself.
Either way the level is rounded to the option it lands on. A scale needs an
order, so `score` refuses options given as an object.

The provider and model are the aria's `system.decide` settings, falling back
to the default outfit's, exactly as for `figaro decide`
([decisions.md](decisions.md)).

## Time

Each method has 1.5 s from the moment the inquiry starts. Methods run in
parallel, so several cost the slowest one, not the sum. Past the deadline, or
on any error, the `default` is written; with no default the key keeps its
value. Measured 2026-10-18 through rou2 for a short template: luna 0.2 to
0.9 s, jev 0.4 to 1.6 s.

An unchanged pick writes nothing. A changed one is an ordinary form patch, so
it shows on the inquiry as a delta (`Δ`) and in `figaro state`. The daemon log
records every run: `method`, the verdict (`decided`, `defaulted`, `kept`),
the value and the time taken.

## The prompt cache comes first

A method changes its key before nearly every request, so a key that the
provider renders ahead of the conversation would cost the whole cached
history each time it moved. With `keep_cache` on (the default), a method runs
only where its change is safe, and is otherwise skipped without a word to the
model:

| Key | Runs when |
|---|---|
| outside `system.` | always: it arrives as a reminder appended to the inquiry |
| `system.thinking_effort` | the route is Anthropic Messages (direct or Copilot) and the model takes per-message effort: Opus 5 and 5.5, Sonnet 5.5, Haiku 5.5, Fable 5.1, Mythos 5.1 |
| any other `system.` key | never |

On those models figaro never sends a top-level `output_config.effort`.
Every inquiry is preceded by a `{"role":"system","content":[],"output_config":{"effort":…}}`
row carrying the effort in force for it, under the
`mid-conversation-output-config-2026-07-01` beta, so a change appends to the
prefix instead of rewriting it. Anthropic documents a top-level change as a
cache miss on every message after it
([prompt caching](https://platform.claude.com/docs/en/build-with-claude/prompt-caching),
[per-message effort](https://platform.claude.com/docs/en/build-with-claude/effort#change-effort-mid-conversation-beta)).
Measured on Copilot's claude-opus-5.5, 2026-10-18: a top-level change after a
thinking turn re-wrote 2,181 tokens of history; the per-message change read
all 10,900.

`keep_cache = false` runs the method anyway and pays for it.

## Overriding

A method owns its key while it is enabled: a `figaro set` of that key holds
until the next inquiry. To pin a value, turn the method off
(`figaro set system.methods.reasoning-effort.enabled false`) and set the key.

Methods are read from the aria's `system.methods` laid over the default
outfit's, leaf by leaf, the same fallback the `system.decide` settings use. An
aria minted before the outfit gained a method still runs it, and anything the
aria sets (`enabled = false`, a different `default`) wins.
