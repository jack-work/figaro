# Campaign notes, copied in because they were not under version control

Copied from `~/notes/figaro` by aria f3aa1d0b (role @980dc16c),
2026-08-18, at the measurement arm's suggestion and on the following
finding.

THESE FILES ARE CITED BY `plans/delta-seam.md` AS THE AUTHORITY FOR
STANDING RULES — the instrument notebook and its sixteen worked
instances, the within-run/between-run distinction that governs which
benchmark results are admissible, the dated REFUSED-NOT-MISSING items and
their thresholds. Until this commit they lived only in a home directory,
with no version, no history and no copy.

WHY THEY WERE NOT VERSIONED, DIAGNOSED RATHER THAN ASSUMED: `~/notes` is
not a git repository. Git commands run there walk UP and resolve to
`$HOME`, which carries a bare repository's layout at its top level (HEAD,
config with core.bare, objects/, refs/, description), created
2026-08-16. It holds ZERO refs and ZERO objects — an empty repo, almost
certainly `git init --bare` run once in the wrong directory. So `git add`
in `~/notes` does not create a repository there; it fails with "this
operation must be run in a work tree". That stray repo also means every
git command run anywhere under `$HOME`, in any directory that is not
itself a repo, resolves to it.

THE STRAY REPO IS SURFACED TO THE OWNER AND NOT TOUCHED. It is his home
directory, and the notes directory holds personal material beside these
files.

STATUS OF THIS COPY: a SNAPSHOT, not a move. The originals remain
authoritative and are still being edited by the arias that own them —
each note names its author. Whoever merges this branch should decide
whether the repo copy becomes canonical or is refreshed; a copy that
silently diverges from its original is the same defect as a comment that
outlives its premise, which this campaign has now documented three times.

SCANNED BEFORE COMMITTING for credential patterns (API key prefixes,
private key headers, bearer tokens): zero matches across all 32 files.
