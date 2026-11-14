#!/usr/bin/env bash
set -euo pipefail
cd /var/tmp/figaro-subscription-oauth
go test ./internal/auth -count=1
