# docs/

**Documentation lives in [`../skills/figaro/`](../skills/figaro/).** Start at
its `SKILL.md`, which indexes everything.

Nothing but pointers belongs here. This directory is deliberately not a second
home for documentation, because two homes drift in both directions at once and
this repository has already paid for that once. The reasoning, the budgets and
the rules are in
[`../skills/figaro/updating-docs.md`](../skills/figaro/updating-docs.md); read
it before adding any file anywhere.

The practical reason the docs live under `skills/`: that tree ships inside the
binary at `$out/share/figaro/skills`, so figaro can read its own documentation
on a machine with no checkout. A file here would not.

## The exception

`skills-patch-trap12.md` is a proposal addressed to the repository owner about
his own configuration, not documentation about figaro. It stays here until he
rules on it.
