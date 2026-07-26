#!/usr/bin/env bash
#
# release.sh — cut a figaro release: version, tag, release branch, push,
# GitHub release. It does NOT touch this machine's installed figaro.
#
#   scripts/release.sh minor
#   scripts/release.sh patch --dry-run
#   scripts/release.sh major -m "the great renaming" --notes-file NOTES.md
#
# What it does, in order:
#
#   1. Preflight — right branch, clean tree, not behind the remote, tools present.
#   2. Compute the next version from the latest vX.Y.Z tag (the release history
#      is the truth), and make flake.nix say exactly that — committing a bump
#      only if it differs. Cutting the same version twice is refused.
#   3. Gate on `go build && go vet && go test` (skip with --no-check).
#   4. Write an annotated tag whose message IS the release notes: subject
#      "vX.Y.Z — title", body prose. Opens $EDITOR unless -m/--notes-file.
#   5. Move the `release` branch to the tag — that is what a `nix profile`
#      entry tracking ?ref=release will pick up.
#   6. Push branch + release + tag, then create the GitHub release, reading
#      title and notes straight back out of the tag. One source of truth.
#
# What it deliberately does NOT do: install, upgrade, or restart anything
# here. Follow it yourself, from a terminal, with:
#
#   figaro stop --keep-pids && nix profile upgrade --all
#
# (The upgrade swaps the binary under the running angelus, so it must not
# happen inside an aria that the angelus is hosting.)

set -euo pipefail

BUMP=""
DRY=0
CHECK=1
DO_GH=1
SUBJECT=""
NOTES_FILE=""
REMOTE="origin"
BRANCH="main"
RELEASE_BRANCH="release"

die() { printf 'release: %s\n' "$*" >&2; exit 1; }
say() { printf '\033[1m==>\033[0m %s\n' "$*" >&2; }
run() {
	if [ "$DRY" = 1 ]; then
		printf '   \033[2m[dry-run]\033[0m %s\n' "$*" >&2
	else
		printf '   \033[2m$\033[0m %s\n' "$*" >&2
		"$@"
	fi
}

usage() {
	cat >&2 <<'EOF'
usage: scripts/release.sh <major|minor|patch> [options]

  -m, --message <subject>  Tag subject text (the part after "vX.Y.Z — ").
                           Skips the editor when --notes-file is also given.
      --notes-file <path>  File holding the release-notes body (prose).
                           "-" reads stdin.
  -n, --dry-run            Print every mutation without performing it.
      --no-check           Skip go build/vet/test.
      --no-github          Tag and push, but create no GitHub release.
      --remote <name>      Git remote (default: origin).
      --branch <name>      Branch being released (default: main).

Version comes from the latest vX.Y.Z tag, not from flake.nix; flake.nix is
then made to agree (and committed if it did not).
EOF
	exit 2
}

while [ $# -gt 0 ]; do
	case "$1" in
		major|minor|patch) BUMP="$1" ;;
		-m|--message)      SUBJECT="${2:-}"; shift ;;
		--notes-file)      NOTES_FILE="${2:-}"; shift ;;
		-n|--dry-run)      DRY=1 ;;
		--no-check)        CHECK=0 ;;
		--no-github)       DO_GH=0 ;;
		--remote)          REMOTE="${2:-}"; shift ;;
		--branch)          BRANCH="${2:-}"; shift ;;
		-h|--help)         usage ;;
		*)                 die "unknown argument: $1 (try --help)" ;;
	esac
	shift
done
[ -n "$BUMP" ] || usage

# ---------------------------------------------------------------- preflight

command -v git >/dev/null || die "git not found"
root=$(git rev-parse --show-toplevel 2>/dev/null) || die "not a git repository"
cd "$root"

[ "$DO_GH" = 1 ] && { command -v gh >/dev/null || die "gh not found (or pass --no-github)"; }
[ "$DO_GH" = 1 ] && { gh auth status >/dev/null 2>&1 || die "gh is not authenticated (gh auth login)"; }

cur_branch=$(git rev-parse --abbrev-ref HEAD)
[ "$cur_branch" = "$BRANCH" ] || die "on branch '$cur_branch', expected '$BRANCH' (use --branch)"

git diff --quiet && git diff --cached --quiet \
	|| die "working tree is dirty — commit or stash first"

# Untracked files are not fatal (result/, scratch output), but they will NOT
# be in the tag — and forgetting to `git add` is exactly how a release ships
# without the thing it was cut for.
untracked=$(git ls-files --others --exclude-standard)
if [ -n "$untracked" ]; then
	printf 'release: warning — untracked files will not be in %s:\n' "$BUMP release" >&2
	printf '%s\n' "$untracked" | sed 's/^/    /' >&2
fi

say "fetching $REMOTE"
git fetch --tags --quiet "$REMOTE" || die "git fetch failed"

if git rev-parse --verify --quiet "$REMOTE/$BRANCH" >/dev/null; then
	behind=$(git rev-list --count "$BRANCH..$REMOTE/$BRANCH")
	[ "$behind" = 0 ] || die "$BRANCH is $behind commit(s) behind $REMOTE/$BRANCH — pull first"
fi

# ------------------------------------------------------------------ version

last_tag=$(git tag --list 'v[0-9]*.[0-9]*.[0-9]*' --sort=-v:refname | head -1)
[ -n "$last_tag" ] || last_tag="v0.0.0"

case "$last_tag" in
	v*.*.*) ;;
	*) die "latest tag '$last_tag' is not vX.Y.Z" ;;
esac
IFS=. read -r MAJ MIN PAT <<<"${last_tag#v}"
case "$MAJ$MIN$PAT" in
	*[!0-9]*) die "cannot parse version from tag '$last_tag'" ;;
esac

case "$BUMP" in
	major) MAJ=$((MAJ + 1)); MIN=0; PAT=0 ;;
	minor) MIN=$((MIN + 1)); PAT=0 ;;
	patch) PAT=$((PAT + 1)) ;;
esac
VERSION="$MAJ.$MIN.$PAT"
TAG="v$VERSION"

git rev-parse --verify --quiet "refs/tags/$TAG" >/dev/null \
	&& die "$TAG already exists locally — nothing to cut"

say "$last_tag  ->  $TAG   ($BUMP)"

unreleased=$(git log --oneline "$last_tag..$BRANCH" 2>/dev/null || true)
[ -n "$unreleased" ] || die "no commits since $last_tag — nothing to release"
printf '%s\n' "$unreleased" | sed 's/^/    /' >&2

# ----------------------------------------------------------------- flake.nix

hits=$(grep -c '^ *version = "[0-9]\+\.[0-9]\+\.[0-9]\+";' flake.nix || true)
[ "$hits" = 1 ] || die "expected exactly one version line in flake.nix, found $hits"
flake_version=$(sed -n 's/^ *version = "\([0-9.]*\)";.*/\1/p' flake.nix)

if [ "$flake_version" = "$VERSION" ]; then
	say "flake.nix already at $VERSION"
else
	say "flake.nix $flake_version -> $VERSION"
	if [ "$DRY" = 1 ]; then
		printf '   \033[2m[dry-run]\033[0m edit flake.nix + commit\n' >&2
	else
		sed -i "s/^\( *version = \)\"[0-9.]*\";/\1\"$VERSION\";/" flake.nix
		git add flake.nix
		git commit -q -m "chore: figaro $VERSION (flake version)"
	fi
fi

# --------------------------------------------------------------------- gate

if [ "$CHECK" = 1 ]; then
	say "go build / vet / test"
	if [ "$DRY" = 1 ]; then
		printf '   \033[2m[dry-run]\033[0m go build ./... && go vet ./... && go test ./...\n' >&2
	else
		go build ./... || die "go build failed"
		go vet ./...   || die "go vet failed"
		go test ./...  || die "go test failed"
	fi
else
	say "skipping build/test gate (--no-check)"
fi

# ---------------------------------------------------------------------- tag

tmpl=$(mktemp); trap 'rm -f "$tmpl"' EXIT

if [ -n "$NOTES_FILE" ]; then
	[ -n "$SUBJECT" ] || die "--notes-file requires -m/--message"
	printf '%s — %s\n\n' "$TAG" "$SUBJECT" >"$tmpl"
	if [ "$NOTES_FILE" = "-" ]; then cat >>"$tmpl"; else cat "$NOTES_FILE" >>"$tmpl"; fi
	tag_args=(-F "$tmpl")
else
	# Editor template. Comment lines are stripped by --cleanup=strip.
	{
		printf '%s — %s\n\n\n' "$TAG" "$SUBJECT"
		printf '# Subject above: "%s — <title>". Body below: prose, not a\n' "$TAG"
		printf '# changelog list. This message becomes the GitHub release notes.\n#\n'
		printf '# Commits since %s:\n' "$last_tag"
		printf '%s\n' "$unreleased" | sed 's/^/#   /'
	} >"$tmpl"
	tag_args=(-e -F "$tmpl")
fi

say "tagging $TAG at $(git rev-parse --short "$BRANCH")"
if [ "$DRY" = 1 ]; then
	printf '   \033[2m[dry-run]\033[0m git tag -a --cleanup=strip %s %s\n' "${tag_args[*]}" "$TAG" >&2
	printf '   \033[2m[dry-run]\033[0m tag message template:\n' >&2
	sed 's/^/       /' "$tmpl" >&2
else
	git tag -a --cleanup=strip "${tag_args[@]}" "$TAG"

	subject=$(git for-each-ref "refs/tags/$TAG" --format='%(contents:subject)')
	body=$(git for-each-ref "refs/tags/$TAG" --format='%(contents:body)')
	case "$subject" in
		*"— "|*"—") git tag -d "$TAG" >/dev/null; die "tag subject has no title — aborted, tag removed" ;;
	esac
	[ -n "${body//[[:space:]]/}" ] || { git tag -d "$TAG" >/dev/null; die "tag body is empty — aborted, tag removed"; }
fi

# ------------------------------------------------------- release branch + push

old_release=$(git rev-parse --short "$RELEASE_BRANCH" 2>/dev/null || echo "none")
say "moving $RELEASE_BRANCH ($old_release -> $(git rev-parse --short "$BRANCH"))"
run git branch -f "$RELEASE_BRANCH" "$BRANCH"

say "pushing to $REMOTE"
if ! run git push "$REMOTE" "$BRANCH" "$RELEASE_BRANCH"; then
	cat >&2 <<EOF
release: push failed. Local state is ahead of $REMOTE. To undo:
    git tag -d $TAG
    git branch -f $RELEASE_BRANCH $old_release
EOF
	exit 1
fi
run git push "$REMOTE" "$TAG"

# ----------------------------------------------------------- GitHub release

if [ "$DO_GH" = 1 ]; then
	say "creating GitHub release $TAG"
	if [ "$DRY" = 1 ]; then
		printf '   \033[2m[dry-run]\033[0m gh release create %s --verify-tag --title ... --notes ...\n' "$TAG" >&2
	else
		gh release create "$TAG" --verify-tag \
			--title "$(git for-each-ref "refs/tags/$TAG" --format='%(contents:subject)')" \
			--notes  "$(git for-each-ref "refs/tags/$TAG" --format='%(contents:body)')"
	fi
else
	say "skipping GitHub release (--no-github)"
fi

# ------------------------------------------------------------------- coda

cat >&2 <<EOF

$TAG is cut and pushed. Nothing on this machine was upgraded.

  consumers pinning immutably:  github:jack-work/figaro/$TAG
  consumers tracking the ref:   ?ref=$RELEASE_BRANCH  (now $TAG)

To move this machine onto it, from a terminal — not from inside an aria:

  figaro stop --keep-pids && nix profile upgrade --all
EOF
