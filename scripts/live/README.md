# Live scripts: driving the real thing

Each of these starts a real daemon against a real store and drives real
verbs. They exist because **a green unit suite is not evidence about the
product**: between them they found nine bugs during the state-layer work
(session 3), and four of those were in the direction that loses data, a
libretto stump drawn in `fig ls`, a fork entry point the CLI uses and the
tests did not, a repair command that declined to repair, a study of a deleted
form that could not be dropped after a restart.

They were kept in `/var/tmp` at first, which is where they would have been
lost. They are here so the next person has them.

| script | what it drives | what it caught |
|---|---|---|
| `studylive.sh` | study, fork, drop, `doctor librettos` on a fresh store | the libretto stump appearing in `ls -g`; `ForkWith` missing the refcount participant |
| `renderlive.sh` | what the MODEL is sent, from the wire dump: deltas, machinery, and the death | the studied block frozen at its first version on every real turn; a studied form's DEATH never reaching the model at all |
| `restartlive.sh` | a studied form across a daemon restart | a study window closed by an assistant record, losing the change permanently |
| `realstudy.sh` | the same against a COPY OF THE REAL STORE (715 rows) | the migration: eleven pre-existing studies with no libretto |
| `migratelive.sh` | boot, then watch `doctor mem` | that the migration is observable without stopping the daemon |
| `castlive.sh` | `fig cast`, the role pointing back, the study | the self-cast path end to end after it left the actor loop |
| `sweeplive.sh` | the segment cache's idle sweep, with heads PINNED | that the first green run was proving figwal's head unload, not the sweep |
| `lazylive.sh` | listing cost and `doctor mem` on the real store | the segment cache's live numbers |
| `idlemem.sh` | PSS of an idle daemon, before and after | that the base never returns its arena (251 → 259 MB) and this build does (141 → 51) |
| `onappendlive.sh` | the fig IR write path translating an entry AS IT LANDS, discriminated by a `study` that appends an entry and sends nothing | a SECOND translation at one FigaroLT: the write path rendered the assistant message that the provider was about to commit natively, and a warm read serves the FIRST -- so the model would have been shown unsigned text with the signed original unreachable beneath it |
| `fdcount.sh` | file descriptors held by a listing | that lazy segment opening does NOT save descriptors, killing a claim I had made in a commit message |

## Rules

- **They copy the real store** (~300 MB each, reflinked) and none cleans up:
  a failed run's store is usually the thing you want to look at. `rm -rf
  /var/tmp/figstudy.* figreal.* figmig.* figcast.* figsweep.* figlazy.*
  figidle.* figfd.*` when you are done. Mine reached 6.6 GB in one session.
- **Build to a temp path, never `./result/bin/figaro`**, which is whatever
  the last `nix build` produced. I read stale output from it as a real
  result once; the giveaway was wording I had changed an hour earlier.
- **There is no `figaro serve`.** The daemon auto-starts on the first CLI
  call. Scripts that ran one were greping an empty log, which is a check
  that cannot fail.
- **Run them one at a time.** They start daemons and copy hundreds of
  megabytes; two at once measures the disk, not the change.
