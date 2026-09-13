#!/bin/bash
set -e

echo "[start] Browser MCP Server starting..."

# Check if Lightpanda is available
if [ -f "/opt/lightpanda/lightpanda" ]; then
    echo "[start] Starting Lightpanda browser..."
    /opt/lightpanda/lightpanda serve --host 0.0.0.0 --port 9222 &
    LIGHTPANDA_PID=$!

    # Wait for Lightpanda to be ready
    echo "[start] Waiting for Lightpanda to initialize..."
    sleep 3

    # Check if Lightpanda is running
    if ! kill -0 $LIGHTPANDA_PID 2>/dev/null; then
        echo "[warn] Lightpanda failed to start, continuing without it..."
    else
        echo "[start] Lightpanda is running on port 9222"
    fi
else
    echo "[warn] Lightpanda not found at /opt/lightpanda/lightpanda"
    echo "[warn] Browser MCP will attempt to connect to external CDP endpoint"
fi

# Start the MCP server
echo "[start] Starting Browser MCP server..."
exec /usr/local/bin/browser-mcp -config /app/configs/config.json
