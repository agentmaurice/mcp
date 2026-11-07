# Test data

- `v01_calls.json`: v0.1 tool calls (no template).
- `v01_origin_main.json`: the JSON-RPC responses of the server at origin/main
  (d793148) for those calls, produced by running that code (`git archive`
  of origin/main plus a dump program). `internal/mcpserver` compares the current
  responses with it (`TestV01ResponsesMatchOriginMain`).
- `contract/`: see `contract/README.md`.
