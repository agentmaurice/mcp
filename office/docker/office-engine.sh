#!/bin/sh
# Launcher of the document engine CLI (Node.js bundle built from source).
exec node /opt/office-engine/cli/genoffice.cjs "$@"
