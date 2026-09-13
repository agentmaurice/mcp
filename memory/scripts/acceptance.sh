#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR=$(cd "$(dirname "$0")/.." && pwd)
TMP_DIR=$(mktemp -d)

cleanup() {
  rm -rf "$TMP_DIR"
}
trap cleanup EXIT

export MEMORY_BASE_DIR="$TMP_DIR"
export MCP_TENANT_ID="acceptance"
export LOGGING_LEVEL="error"
export ROOT_DIR

python3 - <<'PY'
import json
import os
import subprocess
import sys
import threading
import time

root = os.environ.get("ROOT_DIR")
if not root:
    root = os.path.abspath(os.path.join(os.path.dirname(__file__), ".."))

cmd = ["go", "run", "-tags", "duckdb", "./cmd/memory", "-transport=stdio"]
proc = subprocess.Popen(
    cmd,
    cwd=root,
    stdin=subprocess.PIPE,
    stdout=subprocess.PIPE,
    stderr=subprocess.PIPE,
    text=True,
)

def drain(stream):
    for line in stream:
        sys.stderr.write(line)

threading.Thread(target=drain, args=(proc.stderr,), daemon=True).start()

request_id = 1

def call_tool(name, args):
    global request_id
    msg = {
        "jsonrpc": "2.0",
        "id": request_id,
        "method": "tools/call",
        "params": {
            "name": name,
            "arguments": args,
        },
    }
    req_id = request_id
    request_id += 1
    proc.stdin.write(json.dumps(msg) + "\n")
    proc.stdin.flush()
    while True:
        line = proc.stdout.readline()
        if not line:
            raise RuntimeError("Server exited unexpectedly")
        resp = json.loads(line)
        if resp.get("id") == req_id:
            return resp

now = time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime())

upsert_resp = call_tool(
    "memory.entities.upsert",
    {
        "tenant_id": "acceptance",
        "entityType": "customer",
        "items": [
            {
                "externalId": "CUST-001",
                "name": "ACME",
                "attributes": {"country": "FR", "email": "contact@acme.fr"},
            }
        ],
        "source": {"system": "acceptance", "timestamp": now, "author": "tester", "traceId": "t-accept-1"},
        "writer": {"appId": "acceptance", "actorId": "tester", "actorType": "human"},
    },
)

if "result" not in upsert_resp:
    raise RuntimeError("upsert_entities failed")

append_resp = call_tool(
    "memory.facts.append",
    {
        "tenant_id": "acceptance",
        "factType": "invoice_paid",
        "subject": {"entityType": "customer", "externalId": "CUST-001"},
        "payload": {"invoiceId": "INV-88", "amount": 1200.5, "currency": "EUR"},
        "effectiveAt": now,
        "source": {"system": "acceptance", "timestamp": now, "author": "tester", "traceId": "t-accept-2"},
        "writer": {"appId": "acceptance", "actorId": "tester", "actorType": "human"},
    },
)

if "result" not in append_resp:
    raise RuntimeError("append_fact failed")

query_resp = call_tool(
    "memory.query",
    {
        "tenant_id": "acceptance",
        "sql": "SELECT name FROM v_entities WHERE entity_type = 'customer' LIMIT 5",
        "maxRows": 5,
    },
)

structured = query_resp.get("result", {}).get("structuredContent")
if not structured or "rows" not in structured:
    raise RuntimeError("memory.query did not return structuredContent")

print("Acceptance OK: entities.upsert + facts.append + query")

proc.terminate()
proc.wait(timeout=5)
PY
