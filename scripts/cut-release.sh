#!/usr/bin/env bash
set -euo pipefail

usage() {
	cat >&2 <<'USAGE'
usage: scripts/cut-release.sh --version X.Y.Z --sha <commit> --notes <file> [--title <text>] [--dry-run]

Fast-forwards main to <commit>, cuts vX.Y.Z with scripts/release.sh (the bump
kind is derived from the latest tag and must reach exactly X.Y.Z), then checks
the tag, main and release on keel and the tag on GitHub, and prints the tag
object and the release commit.

  --title <text>   tag subject; default is the first line of the notes file
  --dry-run        check everything, change nothing

Run it from the repository's main worktree, on main, clean. Gates are the
caller's business: run them first (go test -race and nix build at <commit>).
The GitHub release and its archives arrive minutes later from the release
workflow; check with: gh release view vX.Y.Z --json assets --jq '.assets|length'
USAGE
	exit 2
}

die() { printf 'cut-release: %s\n' "$*" >&2; exit 1; }

VERSION="" SHA="" NOTES="" TITLE="" DRY=0
while [ $# -gt 0 ]; do
	case "$1" in
		--version) VERSION="${2:-}"; shift ;;
		--sha) SHA="${2:-}"; shift ;;
		--notes) NOTES="${2:-}"; shift ;;
		--title) TITLE="${2:-}"; shift ;;
		--dry-run) DRY=1 ;;
		-h|--help) usage ;;
		*) die "unknown argument: $1" ;;
	esac
	shift
done
[ -n "$VERSION" ] && [ -n "$SHA" ] && [ -n "$NOTES" ] || usage
VERSION="${VERSION#v}"
[[ "$VERSION" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || die "version must be X.Y.Z, got $VERSION"
[ -s "$NOTES" ] || die "notes file $NOTES is missing or empty"
TAG="v$VERSION"

cd "$(git rev-parse --show-toplevel)"
[ "$(git rev-parse --abbrev-ref HEAD)" = main ] || die "not on main"
[ -z "$(git status --porcelain --untracked-files=no)" ] || die "tracked files are dirty"
command -v gh >/dev/null || die "gh is not installed"

git fetch -q keel --tags
FULL=$(git rev-parse --verify "$SHA^{commit}") || die "no commit $SHA"
git merge-base --is-ancestor main "$FULL" || die "main is not an ancestor of $SHA: not a fast-forward"
git rev-parse -q --verify "refs/tags/$TAG" >/dev/null && die "$TAG already exists"
[ "$(git rev-parse main)" = "$(git rev-parse keel/main)" ] || die "local main and keel/main differ"

LATEST=$(git tag --list 'v[0-9]*.[0-9]*.[0-9]*' --sort=-v:refname | head -n1)
[ -n "$LATEST" ] || die "no release tags to compare with"
IFS=. read -r LMAJ LMIN LPAT <<<"${LATEST#v}"
IFS=. read -r NMAJ NMIN NPAT <<<"$VERSION"
if [ "$NMAJ" -eq $((LMAJ + 1)) ] && [ "$NMIN" -eq 0 ] && [ "$NPAT" -eq 0 ]; then BUMP=major
elif [ "$NMAJ" -eq "$LMAJ" ] && [ "$NMIN" -eq $((LMIN + 1)) ] && [ "$NPAT" -eq 0 ]; then BUMP=minor
elif [ "$NMAJ" -eq "$LMAJ" ] && [ "$NMIN" -eq "$LMIN" ] && [ "$NPAT" -eq $((LPAT + 1)) ]; then BUMP=patch
else die "$TAG is not the next major, minor or patch after $LATEST"
fi

[ -n "$TITLE" ] || TITLE=$(head -n1 "$NOTES" | sed 's/^[#[:space:]-]*//')
[ -n "$TITLE" ] || die "no title: pass --title"

printf 'cut-release: %s -> %s (%s after %s), %s commits on main\n' "$(git rev-parse --short main)" "$(git rev-parse --short "$FULL")" "$BUMP" "$LATEST" "$(git rev-list --count "main..$FULL")"
if [ "$DRY" = 1 ]; then
	printf 'cut-release: dry run, nothing changed. Would run:\n  git merge --ff-only %s\n  scripts/release.sh %s --no-check -m %q --notes-file %s\n' "$FULL" "$BUMP" "$TITLE" "$NOTES"
	exit 0
fi

git merge --ff-only "$FULL"
scripts/release.sh "$BUMP" --no-check -m "$TITLE" --notes-file "$NOTES"

git fetch -q keel --tags
RELEASED=$(git rev-parse "refs/tags/$TAG^{commit}")
TAGOBJ=$(git rev-parse "refs/tags/$TAG")
for ref in refs/heads/main refs/heads/release; do
	got=$(git ls-remote keel "$ref" | cut -f1)
	[ "$got" = "$RELEASED" ] || die "keel $ref is ${got:-missing}, want $RELEASED"
done
[ "$(git ls-remote keel "refs/tags/$TAG" | cut -f1)" = "$TAGOBJ" ] || die "keel does not carry $TAG at $TAGOBJ"

slug=$(git remote get-url github 2>/dev/null | sed -E 's#^(git@github.com:|https://github.com/)##; s#\.git$##')
[ -n "$slug" ] || slug=jack-work/figaro
gh_obj=$(gh api "repos/$slug/git/ref/tags/$TAG" --jq .object.sha 2>/dev/null || true)
[ "$gh_obj" = "$TAGOBJ" ] || die "GitHub ($slug) has tag object ${gh_obj:-none}, want $TAGOBJ"

printf 'tag %s\nobject %s\ncommit %s\n' "$TAG" "$TAGOBJ" "$RELEASED"
printf 'release assets arrive from the workflow; check: gh release view %s -R %s --json assets --jq ".assets|length"\n' "$TAG" "$slug"
