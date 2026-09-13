# MCP System

MCP System gives AgentMaurice Agents bounded primitives to diagnose the VM that
hosts the runtime and, after explicit authorization, to apply a few allowlisted
operational actions.

The repository produces two Go binaries:

- `system`: non-root STDIO MCP server, executed in the sidecar;
- `system-host-agent`: local privilege boundary, exposed only through the Unix
  socket `/run/agentmaurice/system.sock`.

There is no free-form shell command, no arbitrary filesystem access and no TCP
port. v0.1 actions are limited to `service_restart` and `runtime_restart`.

## Tools v1

- `system_health_v1`
- `system_capabilities_v1`
- `system_inspect_v1`
- `system_services_v1`
- `system_logs_v1`
- `system_action_plan_v1`
- `system_action_apply_v1`

## Host agent configuration

| Variable | Default | Role |
|---|---|---|
| `SYSTEM_AGENT_SOCKET` | `/run/agentmaurice/system.sock` | Local Unix socket |
| `SYSTEM_MUTATION_MODE` | `disabled` | `disabled` or `standing_grant` |
| `SYSTEM_ALLOWED_SERVICES` | empty | Comma-separated systemd units |
| `SYSTEM_RUNTIME_RESTART_SCRIPT` | empty | Canonical script, absolute path |
| `SYSTEM_MAX_LOG_LINES` | `500` | Server-side bound, maximum 2000 |
| `SYSTEM_PLAN_TTL` | `5m` | Validity duration of a plan |

The sidecar uses `SYSTEM_AGENT_SOCKET` and may set `SYSTEM_ACTOR` for
traceability. Access to the socket must be restricted to the runtime's system
group.

## Development

```bash
task fmt
task vet
task test
task build
```

The sidecar image is published on SemVer tags.

## License

Apache-2.0.
