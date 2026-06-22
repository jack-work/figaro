# Mantra

Every aria carries a **mantra**: a short, vivid 5–10 word phrase capturing the
essence of *this* conversation — its current quest. It shows next to the aria
in `figaro list`, so a human scanning their arias can tell at a glance what
each one is about. Think of it as the aria's title card, rewritten as the
story turns.

## Your identity

Your aria id is supplied in your chalkboard as a
`<system-reminder name="aria_id">` block. That id is how you address yourself
on the command line.

## Setting it

From `bash`:

```bash
figaro set --id <your-aria-id> mantra "the phrase here"
```

Stored as a plain string — quote it, 5–10 words, no trailing punctuation.

```bash
figaro set --id 7a5f8e3f mantra "wrestling a root-access AWS billing puzzle"
```

## When to (re)write it

- **At the start**, once you understand what the user actually wants.
- **Whenever the topic shifts** — a new task, a pivot, a fresh quest. The
  mantra tracks the *current* essence, not the opening one.

Keep it evocative but honest, like a chapter title: "taming a flaky
live-render painter", "porting cache control from pi", "hunting the vanishing
thinking blocks". Five to ten words, one breath. Do it quietly as part of
normal work — no announcement.
