# AgentMaurice Guard MCP

Deterministic Go MCP server for detecting, masking and classifying sensitive
data. It complements MCP Moderator without replacing it: Moderator handles
content safety, Guard handles PII, secrets and enterprise classification.

Tools:

- `guard_health_v1`
- `guard_capabilities_v1`
- `guard_scan_v1`
- `guard_redact_v1`
- `guard_classify_v1`

Scan results only expose the type and position of each detection, never its
value. v0.1 accepts at most 1 MiB of inline text.

## License

Apache-2.0.
