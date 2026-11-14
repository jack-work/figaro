#!/usr/bin/env bash
set -euo pipefail
cd /var/tmp/figaro-subscription-oauth
go test -race ./internal/auth -count=3
