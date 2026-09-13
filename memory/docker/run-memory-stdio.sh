#!/bin/sh
set -eu

exec /usr/local/bin/mcp-memory -transport stdio "$@"
