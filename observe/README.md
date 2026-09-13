# AgentMaurice Observe MCP

Deterministic diagnostic Go MCP server for OTLP JSON exports and already
redacted run summaries. It normalizes a timeline, computes the critical path
and highlights errors, retries and regressions between two runs.

Tools:

- `observe_health_v1`
- `observe_capabilities_v1`
- `observe_run_v1`
- `observe_explain_v1`
- `observe_compare_v1`

v0.1 does not connect directly to Tempo, Loki, Prometheus or ClickHouse. The
inline JSON input is capped at 2 MiB and must contain no raw prompt, no
credential and no sensitive business payload.

## License

Apache-2.0.
