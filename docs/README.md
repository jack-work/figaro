# docs/

**Documentation lives in [`../skills/figaro/`](../skills/figaro/).** Start at
its `SKILL.md`, which indexes everything.

Nothing but pointers belongs here. This directory is deliberately not a second
home for documentation, because two homes drift in both directions at once and
this repository has already paid for that once. The reasoning, the budgets and
the rules are in
[`../skills/figaro/contributing/updating-docs.md`](../skills/figaro/contributing/updating-docs.md); read
it before adding any file anywhere.

The practical reason the docs live under `skills/figaro/`: that tree is
EMBEDDED in the binary (`skills.go`) and unpacked on first use, so figaro can
read its own documentation on a machine with no checkout and with nothing
installed beside the executable. A file here would not travel at all.

## The exception

`skills-patch-trap12.md` is a proposal addressed to the repository owner about
his own configuration, not documentation about figaro. It stays here until he
rules on it.
