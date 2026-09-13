#!/bin/sh
set -eu
# Permit only the synthetic embedding fixture, not the whole private network.
fixture_ip="$(getent hosts embedder | awk 'NR == 1 {print $1}')"
test -n "$fixture_ip"
export MEILI_EXPERIMENTAL_ALLOWED_IP_NETWORKS="$fixture_ip/32"
exec /bin/meilisearch
