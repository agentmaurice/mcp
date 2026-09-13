# AgentMaurice Data MCP

Go MCP server for profiling and running one-off queries on CSV or JSON tables,
without arbitrary SQL and without ingestion into MCP Memory.

Tools:

- `data_health_v1`
- `data_capabilities_v1`
- `data_profile_v1`
- `data_query_v1`
- `data_export_v1`

v0.1 accepts inline content bounded to 4 MiB, 10,000 rows and 200 columns.
Queries use a closed JSON plan only (projection, filters, sort, limit and
aggregations). A result larger than 512 KiB is rejected with `buffer_required`
until the managed Buffer is wired in.

## License

Apache-2.0.
