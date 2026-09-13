# AgentMaurice API MCP

Go MCP server for calling internal APIs described by an approved OpenAPI 3.0
or 3.1 contract. It only invokes a known `operationId`; it is not a generic
HTTP proxy.

Tools:

- `api_health_v1`
- `api_capabilities_v1`
- `api_describe_v1`
- `api_call_v1`

Allowed hosts are provided through `MCP_API_ALLOWED_HOSTS`. HTTPS is required;
literal IP addresses, credentials embedded in the URL and redirects are
rejected. Mutations require `allow_mutation=true`. A `secret://...`
`credential_ref` is resolved from the sidecar environment without ever exposing
its value to the model.

## License

Apache-2.0.
