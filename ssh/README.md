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

There is no PTY, no SCP, no port forwarding and no free-form destination.
File transfer is limited to SFTP publication under a target's declared
`publish_root` (see below).

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

## SFTP publication (AGENT-1587)

A published target may declare an optional `publish_root` (clean absolute
path, never `/`). Without it, the three tools below answer
`publish_not_enabled`. There is still no free-form destination: the tools take
a `target` and a path relative to that root, and reuse the target's credential,
strict host-key check and timeouts over the existing SSH connection (SFTP
subsystem via `github.com/pkg/sftp`).

- `ssh_upload_v1` `{target, path, file:{content_base64}, overwrite?:false, mode?:"0644"}`
  returns `{status, target, path, bytes, sha256}`. `mode` must keep owner read
  and write (`0600` or wider): the upload reads the file back to check its hash.
- `ssh_remove_v1` `{target, path}` returns `{status, removed}` (files only).
- `ssh_list_v1` `{target, path?}` returns `{entries:[{path, bytes, mode, modified}], truncated}`
  (depth 1, 500 entries).

Confinement: the path must be relative, contain no `..`, backslash or NUL, and
is cleaned. Every existing directory component is checked with `lstat`; any
symbolic link is refused (`path_outside_root`), and the resolved directory must
stay under the resolved `publish_root`. Uploads are capped at a fixed 20 MiB, written to a hidden temporary file in the target
directory, read back and compared by sha256, then renamed (`posix-rename`
; required for `overwrite:true`, otherwise `atomic_replace_unavailable` and nothing is changed). `overwrite`
defaults to false (`file_exists`). `file.ref` (Buffer) returns
`buffer_unavailable`: this server has no Buffer client yet.

Projection field: `"publish_root": "/var/www/site"`. `ssh_targets_v1` exposes
only `publish_enabled`, never the root.

### Deployment requirements

The `Lstat` and `RealPath` confinement does not prevent a directory
substitution between the check and the use by someone who can already write
inside `publish_root`. Use a dedicated publication account limited to
`publish_root`: either `ChrootDirectory` with `ForceCommand internal-sftp`, or
Unix permissions giving it no read or write access elsewhere. Minimal
`sshd_config`:

```text
Match User publish
    ChrootDirectory /var/www
    ForceCommand internal-sftp
    AllowTcpForwarding no
    X11Forwarding no
```

(the chroot directory must be root-owned and not group-writable; set
`publish_root` to a path inside the chroot, for example `/site`.)

The 500-entry limit of `ssh_list_v1` bounds the response, not the remote
read: pkg/sftp v1.13 has no incremental directory read, so the whole directory
is read before truncation.

## Host key pinning and key types

`host_key_fingerprint` is a bare `SHA256:` fingerprint and carries no key
type. When a server offers several host keys (for example RSA and ed25519),
Go would negotiate RSA and an ed25519 pin would fail. The optional target
field `host_key_type` (`ssh-ed25519`, `rsa`, `ecdsa-sha2-nistp256`,
`ecdsa-sha2-nistp384`, `ecdsa-sha2-nistp521`) restricts `HostKeyAlgorithms`
to that key family. Without it the connector tries the default negotiation,
then one restricted attempt per family, and only our own host-key rejection, or a failure before authentication started after such a rejection, moves on
to the next attempt (authentication is never retried). The fingerprint is
checked on every attempt, so the pin is never weakened.

## Injected call context

The One adds `deployment_id`, `organization_id`, `user_id`, `space_id` and
`session_id` to every call. These keys are dropped before the strict argument
decoding, except `session_id` where the tool declares it (session tools).
Any other unknown argument is still refused.

## Development

```bash
task check
```

## License

Apache-2.0.
