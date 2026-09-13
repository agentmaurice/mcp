#!/bin/sh
set -eu

exec /usr/local/bin/rag -transport stdio "$@"
