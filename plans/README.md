# plans

The plan archive moved to the `history` branch on 2026-10-12. A plan is an
intent document and a historical record once the work lands; the code and
`ARCHITECTURE.md` say what shipped.

```bash
git ls-tree -r --name-only history plans/      # what there is
git show history:plans/<name>.md               # read one
git show history:plans/campaign-notes/<name>.md
```

The files still in this directory are the ones cited by path from Go
comments, shipped skills (`skills/figaro/**`), or `scripts/`. They stay so
those citations resolve; do not add to them. New design thinking goes in
`duck/`.
