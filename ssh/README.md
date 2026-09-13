# MCP SSH

Go MCP server for governed remote administration over published SSH targets.
The Run only picks a known `target_id`; the destination, identity, host key and
credential reference stay in the local projection.

## Tools v0.1

- `ssh_health_v1`
- `ssh_capabilities_v1`
- `ssh_targets_v1`
- `ssh_target_check_v1` (Build only)
- `ssh_session_open_v1`
- `ssh_exec_v1`
- `ssh_session_status_v1`
- `ssh_session_close_v1`

v0.1 exposes no PTY, no SCP/SFTP, no port forwarding and no free-form
destination.

## Configuration

```text
SSH_EXECUTION_MODE=run
SSH_TARGETS_FILE=/etc/agentmaurice/ssh-targets.json
SSH_CREDENTIALS_FILE=/run/secrets/ssh-credentials.json
SSH_ALLOWED_TARGETS=production-api,legacy-erp
```

Projection example:

```json
{
  "revision": "2026-09-01.1",
  "targets": [
    {
      "target_id": "production-api",
      "label": "Production API",
      "host": "10.20.0.12",
      "port": 22,
      "username": "agent-ops",
      "host_key_fingerprint": "SHA256:...",
      "credential_ref": "ssh-production-api",
      "enabled": true,
      "published": true,
      "policy_tags": ["production"]
    }
  ]
}
```

The file provider is a minimal standalone boundary. Its file must be a regular
file readable only by its owner (`0600`). Centralized AgentMaurice secret
management is deliberately deferred; a future provider can be plugged in
without changing the MCP tools.

## Development

```bash
task check
```

## License

Apache-2.0.
