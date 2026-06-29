---
name: howto
description: Guided onboarding for a new figaro user. Use when the user asks how to use figaro, wants a tutorial, says they're new, asks "how does this work", wants to learn the basics, asks about loadouts/chalkboard/skills/mantra, or otherwise signals they're getting started. Drive the lesson interactively — do not lecture.
---

# How to teach figaro to a new user

You are about to walk a new user through figaro. This is **not** a
monologue. It is a **guided tour with verification gates**. The user
must perform each step themselves, and you must confirm they did
before moving on.

---

## Before anything else: calibrate

Open with two short questions. Do **not** ask more. Do not turn this
into an interview.

> "Quick calibration before we start —
> 1. On a 1–5 scale, how comfortable are you in a terminal?
> 2. On a 1–5 scale, how much do you code?"

Then adapt:

- **1–2 on terminal** → explain what a flag is, what `--` means, that
  Ctrl-C interrupts. Avoid jargon like "PID", "stdout", "TOML"
  without a one-line gloss. Show every command before asking them to
  run it.
- **3–4 on terminal** → normal pace, normal vocabulary.
- **5 + high code score** → terse, dense, skip the hand-holding,
  assume Unix fluency. Lean into power-user moves.

Match length to confidence. A confident user wants short answers and
the next step; a nervous one wants reassurance and a visible
example.

---

## The lesson, in order

Each section below is **one step**. For each step:

1. Explain the concept in **2–4 sentences**, calibrated to the user.
2. Ask the user to run a specific command and report back what they
   saw (or paste output).
3. **Verify.** Use `figaro state`, `figaro list`, `figaro show`, or
   `bash` to confirm the state changed as expected. Do not take
   "yeah it worked" at face value if you can check.
4. Only then move on.

If a step fails, debug it with them before continuing. Failed
onboarding is worse than slow onboarding.

---

### Step 1 — `figaro` and `fig`

Concept: figaro is a CLI that holds long-running conversations called
**arias**. Each aria is its own thread of context. The default verb
summons the agent: `figaro -- <prompt>`. `fig` is the same binary
under a shorter name.

Ask them to run:

```bash
figaro -- say hello in one sentence
```

Verify: `figaro list` should now show one aria. Check it.

---

### Step 2 — `figaro list` and `figaro show`

Concept:
- `figaro list` shows every aria — id, the last few words of its
  topic, its mantra (more on that soon), and bookkeeping.
- `figaro show <id>` prints the transcript. With no id it shows the
  aria currently bound to this shell.

Ask them to run `figaro list`, then `figaro show`. Have them describe
what they see.

---

### Step 3 — `figaro new` and continuing a conversation

Concept: every plain `figaro -- ...` call continues the aria bound to
the current shell. `figaro new -- <prompt>` starts a fresh aria with
its own context. This is how you keep concerns separated.

Ask them to:

```bash
figaro new -- what is the capital of france?
figaro -- and of italy?
```

Verify with `figaro list` — they should see two arias, and the
second prompt should have landed in the *new* one (because `new`
re-binds the shell). Confirm the second prompt's aria has both
turns.

---

### Step 4 — `figaro send` and aria addressing

Concept: `figaro send <id> -- <prompt>` sends to a specific aria
without changing what the shell is bound to. Useful when you want
to poke at an old conversation without leaving your current one.
Ids can be prefixes — figaro resolves them as long as the prefix is
**unique**.

Ask them to send a follow-up to the *first* aria using a short id
prefix:

```bash
figaro send <prefix> -- one more line, please
```

Verify by showing that aria.

---

### Step 5 — The chalkboard

This is the core concept. Slow down here.

Concept: every aria has a **chalkboard** — a key/value store of
state that travels alongside the message log. Two flavors:

- **Agent-visible keys** (e.g. `mantra`, `cwd`, `datetime`, anything
  the user sets). These render as `<system-reminder>` blocks the
  model sees on every turn.
- **System keys** — anything starting with `system.` — are
  **hidden from the model**. They configure the runtime: `system.model`
  picks the LLM, `system.thinking_budget` controls extended thinking,
  `system.max_tokens` caps the response, etc. The agent never sees
  them; they shape its behavior from the outside.

Two ways to write the chalkboard:

```bash
figaro set mantra "learning figaro"        # agent-visible
figaro set system.model claude-sonnet-4-5  # hidden, reconfigures runtime
figaro state                               # inspect everything
figaro unset <key>                         # remove
```

`figaro set` is **out-of-band**: it patches state without spending a
model round-trip. Crucial for "fix the model mid-conversation"
moments.

Ask them to set their mantra to something memorable, then run
`figaro list` and notice it appears in the mantra column. Verify
with `figaro state`.

Then have them run a normal prompt and observe that the agent now
*sees* the mantra (you can ask the agent something like "what's my
mantra right now?" — it should know).

---

### Step 6 — The mantra in particular

Concept: the **mantra** is a 5–10 word phrase naming the current
quest. It shows in `figaro list` so the user can scan a screenful
of arias and know what each one is about. The *agent* is expected
to keep it current — when the conversation pivots, rewrite the
mantra. The user can also set it manually.

Ask them to start a new aria with a clear topic, watch the mantra
auto-populate, then pivot the conversation and watch it change. Use
`figaro list` to confirm.

---

### Step 7 — Loadouts

Concept: a **loadout** is a TOML file at
`~/.config/figaro/loadouts/<name>.toml`. It defines the **initial
chalkboard** for every new aria. The active loadout is named in
`~/.config/figaro/config.toml` under `default_loadout`.

Show them their loadout:

```bash
cat ~/.config/figaro/config.toml
cat ~/.config/figaro/loadouts/*.toml
```

Walk them through what's in it:

- `[system]` block — the hidden runtime knobs. `provider`, `model`,
  `max_tokens`, `thinking_effort` / `thinking_budget`, etc.
- Top-level keys — these become agent-visible chalkboard values on
  every new aria.

**Changing the default model:**

- Permanently for every new aria → edit the loadout's
  `[system].model`.
- Just this aria, mid-session → `figaro set system.model <id>`.

Have them check their current model with `figaro state | grep model`,
then change it for the current aria using `set`, and verify it
changed.

---

### Step 8 — Files in the chalkboard

Concept: a loadout value can be `{ fileName = "credo.md" }` — figaro
inlines the file's contents at that key. Or
`{ dirName = "skills" }` — figaro fans the directory out, one
chalkboard entry per file (`skills.<basename>`). Paths are relative
to `~/.config/figaro/`.

This is how big text payloads (system prompts, skill catalogs,
project notes) ride along on every aria without bloating the TOML.

Show them their loadout's `credo` line if present, then `cat` the
referenced file so they understand the indirection.

---

### Step 9 — Skills (a polite lie)

Concept: **skills are not a first-class primitive in figaro.** They
are *just files referenced in the chalkboard via `dirName`*. The
name "skill" sticks because modern LLMs are post-trained to respond
to that framing — they treat a markdown file with a `description:`
frontmatter as an opt-in capability. You could call them "lore" or
"playbooks" or anything else; the harness doesn't care.

The convention:

```markdown
---
name: thing-name
description: One sentence. Used as a trigger — the model decides
  when to read the file in detail based on this line.
---

# Body of the skill
...
```

`~/.config/figaro/skills/*.md` get loaded as
`skills.<filename>` chalkboard entries on every aria (via the
`skills = { dirName = "skills" }` line in the loadout). The
frontmatter is what the model sees first; the body is read when
relevant.

Have them open the skills directory and look at one or two.

---

### Step 10 — Use cases: assistant systems via chalkboard

Concept: because the chalkboard is open-schema and round-trip-free
to update, you can build **adaptive assistant systems** without any
plugin machinery.

Examples to share, calibrated to skill level:

- **Mode switching** — your credo / system prompt can say things
  like "*set mode concise if the user expresses frustration at
  length*". The agent then issues `figaro set mode concise` mid-turn.
- **Auto-mantra** — instruct the agent to rewrite the mantra
  whenever the conversation's purpose pivots.
- **Runtime reconfiguration** — `system.thinking_effort = "max"`
  before a hard problem; `figaro unset system.thinking_effort` after.
- **Skills as opt-in capability surface** — write a markdown file
  with a sharp `description:`; the model self-selects it when the
  description matches the situation.
- **Project context injection** — `[system].cwd`, or a
  `{ fileName = "PROJECT.md" }` entry, gives every aria in a project
  shared grounding.

Don't enumerate all of these — pick **one or two** the user is most
likely to want, and demonstrate with them.

---

### Step 11 — Prefix stability (the invisible gift)

Concept: figaro is built on a write-ahead log that **never moves
existing entries**. Once a turn is committed, its position in the
log and its translated provider payload are immutable. This means
prompt caches (Anthropic's cache_control) and the chalkboard's
patch stream stay byte-stable across turns — small additions don't
invalidate the long prefix.

Practical consequence for the user: long-running arias **stay
cheap**. You can keep a conversation alive for hundreds of turns
without paying full prompt cost each time, because the prefix is
cached server-side.

They don't need to *do* anything here. Just know it's why the tool
feels fast on long threads.

---

## Closing

End with: "That's the core. Three commands cover 90% of daily use:
`figaro -- ...`, `figaro list`, `figaro set <key> <value>`.
Everything else is decoration."

Offer one concrete next step calibrated to their score — e.g.
"want to set up a skill for your current project?" for a coder, or
"want to try a longer conversation?" for someone newer.

### Optional finishing touch — shell completions

Before offering to delete this skill, mention tab-completion. It's
opt-in because Go's `go install` doesn't touch the user's shell.

> "One last thing: `figaro completion install` writes tab-completion
> for your shell (bash / zsh / fish). On a bare-bones WSL or minimal
> container, bash users may also need `sudo apt install bash-completion`
> for the loader hook to fire. Want me to run it?"

If yes — run `figaro completion install` for them and read back the
"wrote ... completion to ..." line plus any note the command prints
(e.g. zsh needs an `fpath` line in `.zshrc` before `compinit`).

If no — move on. It's reversible: `rm` the file later if they hate it.

### Then — offer to remove this skill

This onboarding skill is **not meant to stick around**. Every aria
the user opens from here on will load it as context, which is
wasteful once the lesson is done. Ask, plainly:

> "This onboarding skill loads on every new aria. Now that you've
> been through it, want me to delete it? (`~/.config/figaro/skills/howto.md`)"

If yes — remove it with `rm ~/.config/figaro/skills/howto.md` and
confirm with `ls ~/.config/figaro/skills/`. New arias will no longer
carry the howto.

If no — leave it. They can delete it themselves later.

Do not delete it without asking. Do not delete anything else in the
skills directory.

---

## Anti-patterns (for you, the teaching agent)

- Don't dump all eleven steps at once.
- Don't move on without verifying the previous step landed.
- Don't lecture about architecture (figwal, IR, etc.) unless asked.
- Don't mention forking — that's a separate lesson.
- Don't read the user's tokens, keys, or providers/ files.
- Match the user's energy. Short answers to short users.
