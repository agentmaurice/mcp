#!/bin/sh
set -eu
cd "$(dirname "$0")/.."
project="mcp-search-test-$$"
compose() { docker compose -p "$project" -f testdata/compose.yaml "$@"; }
trap 'compose down --volumes --remove-orphans >/dev/null 2>&1' EXIT INT TERM
compose up -d --wait --wait-timeout 90
export MEILI_TEST_URL="http://$(compose port meili 7700)"
export MEILI_TEST_EMBEDDER_URL="http://embedder:8080"
export MEILI_TEST_MASTER_KEY="search-integration-ephemeral-only"
go test -tags=integration -count=1 -timeout=3m -v ./integration
