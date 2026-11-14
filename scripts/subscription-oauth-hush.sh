#!/usr/bin/env bash
set -euo pipefail
cd /var/tmp/figaro-subscription-oauth
module=$(go list -m -f '{{.Dir}}' github.com/jack-work/hush)
version=$(go list -m -f '{{.Version}}' github.com/jack-work/hush)
test "$version" = v0.14.6
scratch=$(mktemp -d /var/tmp/figaro-auth-hush-bin.XXXXXX)
trap 'rm -rf "$scratch"' EXIT
(cd "$module" && go build -buildvcs=false -o "$scratch/hush" .)
export FIGARO_AUTH_TEST_HUSH="$scratch/hush"
go test -race ./internal/auth -run TestIsolatedHushRotatesAndPersistsDeclaredPKCEGrant -count=3 -v
