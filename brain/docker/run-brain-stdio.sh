#!/bin/sh
set -eu

exec /usr/local/bin/brain -transport stdio "$@"
