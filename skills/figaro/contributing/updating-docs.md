# Updating these docs

Read this before editing anything in this tree. The layout has a rule and the
rule has a reason; without the reason the next person re-splits it on taste.

## The rule

**One tree, three tiers, and every fact in exactly one file.**

| Tier | What | Cost |
|---|---|---|
| 0 | `SKILL.md` | The only file loaded automatically, and only its front-matter. |
| 1 | `start.md`, `cli.md`, `agents.md`, `maintaining.md`, `updating-docs.md` | One deliberate read each. |
| 2 | `reference/`, `notes/` | Opened when working inside that subsystem. |

Higher tiers **name and link**. They never restate. A pointer is fine; a
paraphrase is a future contradiction with a delay fuse.

The one allowed overlap: Tier 0 may print a command **invocation** that Tier 1
explains. An invocation line is a table of contents entry. The moment the index
grows a second line explaining that command, delete it and link instead.

## Why one tree, and not docs plus skill

Because of how the loader actually works. `internal/outfit` reads a skill
directory keyed by its directory name and rooted at `SKILL.md`. If a file
begins with a `---` fence it is enveloped **front-matter only**, and the body
is never put in context; the agent gets a `filePath` and reads the body if it
decides to. Sibling files in the same directory are not loaded at all. They are
inert until something opens them.

So cost is a property of what an agent **reads**, not of where a file **lives**.
Progressive disclosure is depth, not address. And everything under
`skills/figaro/` is embedded in the binary and unpacked on first use, so a
figaro on a machine with no checkout still reaches the deep chapters. Two homes
buys nothing and costs the thing below.

## What two homes costs, with the evidence from this repo

Two copies of a fact drift in **both directions at once**, and we had a live
specimen. The user config copy at `~/.config/figaro/skills/figaro/` shadowed
the shipped skill by name. In it:

- `architecture.md` was 201 changed lines **behind** the repo.
- `SKILL.md` was **ahead**, holding a 178 line section, "Handing work back",
  that existed nowhere in version control. It survives now as
  [maintaining.md](maintaining.md), rescued before anyone proposed deleting the
  shadow.

Neither copy was wrong on purpose. That is the point: nobody chooses drift.

## Budgets, as numbers

A budget without a number is a preference.

| Thing | Budget |
|---|---|
| Front-matter description | around 100 tokens. It is loaded always, for every skill. |
| `SKILL.md` body | under 5k tokens, and under 500 lines. |
| Tier 1 page | one sitting. If it will not fit, it is two topics. |
| Tier 2 file | unlimited, because it is read on demand. |

## Rules that follow

1. **Mutually exclusive paths go in separate files.** Anything bundled together
   is paid for together. If two subsystems are never in play at the same time,
   they must not share a file. This is stricter than one topic per file, and it
   is the rule that actually saves tokens.
2. **A description says WHEN to read, not WHAT is inside.** The description is
   the whole gate: it alone decides whether a file is ever opened. One that
   describes contents either never fires or always fires.
3. **Front-load.** Model accuracy degrades with position and is worst in the
   middle of a long context. The commands an agent needs most go at the top of
   the index, not in a tidy alphabetical list.
4. **One authoritative definition per concept.** Conflicting definitions are a
   documented cause of an agent oscillating between incompatible answers. The
   vocabulary table in `SKILL.md` holds the one-sentence definitions and names
   the file that owns each model.
5. **Separate how-to from explanation.** An agent on an errand must not wade
   through why-it-works to reach the command. Take that one idea from Diátaxis
   and leave the four-quadrant framework alone; at this size the quadrants
   would be empty files.
6. **The admission test is not "is this true".** It is: *would a reader act
   differently without this line?* True but inert prose is not free. It is
   billed on every read, including lines you are fond of.
7. **Describe the invariant, not the diff.** No "recently", no "as of 0.15.2",
   no in-flight branch names, no changelog voice. Something genuinely planned
   and not built says so in a banner at the top, unmissably, the way
   [range-store.md](range-store.md) does.
8. **Check every claim against source, not against the previous doc.** A skill
   that lies is worse than no skill, because it is trusted. Quote the line you
   are describing and name its file.

## What the evidence supports, and what it does not

A controlled study of repository context files (ETH Zurich and LogicStar,
arXiv 2602.11988; four agents over SWE-bench Lite and a new AGENTbench) found
human-written context files bought about +4%, while generated ones were worse
in five of eight settings. The mechanism is the useful part: strip the
repository's **other** documentation and those same generated files started
helping, about +2.7%. They were not wrong. They were duplicative.

The same work measured the cost side: instructions are obeyed **and** billed,
at roughly +2.4 to +3.9 extra steps, +19 to +23% cost, +14 to +22% reasoning
tokens. **Duplication is charged twice and pays once.** That is the empirical
floor under rule 1 and under the no-paraphrase rule.

Two things to be honest about:

- **Nothing published measures what this tree is.** Every source above studies
  one audience at a time. Docs that serve a human and an agent from one file
  are not proven by anyone's evidence. That is a reason to keep this small and
  reversible, not a reason to avoid it.
- **Do not cite these as evidence:** the "63% token savings" figure (marketing,
  no methodology), "tables beat prose for agents", and "one screen per doc".
  They are conventions, and reasonable ones. They are not measurements.

`llms.txt` is the cautionary tale for anyone tempted to add a second index: a
machine-readable index at a well-known path, roughly 10% adoption, no major
consumer confirmed. An index only pays when a runtime actually loads it.
Figaro's binary loads `skills/`, so this index has a real consumer. A parallel
agent-only index would be unpaid overhead.

## Two traps that have already bitten

**The index must not become a summary.** Once `SKILL.md` restates its sections,
there are two copies again and the sections stop being opened. One line per
entry: what it is, and when to read it.

**An untracked file builds clean and silently does not ship.** For a flake,
`$src` is the **git tree**, and `go:embed` walks that tree: a new section file
that has not been `git add`ed produces a binary with the edited `SKILL.md` and
none of the new sections, with no error anywhere. After adding a file, verify
that it is inside the artifact:

```sh
nix build .#default -o /var/tmp/figaro-check
/var/tmp/figaro-check/bin/figaro doctor skills
```

## When you add or move a file

1. Put it in the tier that matches when it is read, not how important it feels.
2. Add exactly one row to the `SKILL.md` table, phrased as *when to read it*.
3. Grep for every fact you moved and prove there is one hit left.
4. Follow every relative link you touched.
5. `git add` it, then build and list the store output.
