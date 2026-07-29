# Security Policy

## Reporting a vulnerability

Please do not publish working exploits, subscription credentials, session
cookies, database files, or server details in a public issue. Use GitHub's
private vulnerability reporting / Security Advisory flow for this repository.
Include the affected version, deployment shape, reproduction steps, expected
impact, and any relevant redacted logs.

No release should be described as free of all vulnerabilities. Security fixes
are prioritized by exploitability, privilege gained, exposed secrets, and
impact on the independent Mihomo data plane.

## Deployment boundary

- The management API binds to loopback by default. Publish it only through an
  authenticated private network, VPN, or hardened reverse proxy with TLS.
- The Mihomo External Controller uses a private Unix Socket in production and
  must not be exposed publicly.
- Public proxy Listeners and the management endpoint are separate. WebSocket
  paths are routing identifiers, not authentication.
- Keep `/var/lib/hx-proxygroup`, the master key, SQLite database, runtime
  configuration, subscription snapshots, and disaster backups readable only
  by the service account and administrators.
- Apply OS, Mihomo, and HX-ProxyGroup security updates together. The project's
  MIT license does not change the licenses or security policies of bundled
  third-party components.

## Browser terminal threat model

The optional browser terminal is a local PTY Shell on the HX-ProxyGroup server;
it is not an SSH client or an SSH jump host. Enabling it grants the logged-in
administrator command execution as the `hx-proxygroup` service account, which
can read and modify the application's persistent state. Treat management-panel
administrator access as equivalent to that OS account while the feature is on.

The terminal is disabled by default and requires all of the following:

- an explicitly configured feature flag;
- a configured, authenticated administrator session;
- same-origin WebSocket acceptance;
- a maximum of two concurrent sessions;
- ten-minute idle and two-hour absolute session limits;
- periodic session revalidation, so logout, username changes, password changes,
  and expiry terminate existing sessions;
- bounded WebSocket frames and PTY window dimensions;
- a minimized Shell environment that excludes control-plane environment
  variables and sets `HISTFILE=/dev/null`;
- structured open/close audit records without command contents or secrets.

Predictive local echo is enabled only when the server reads both `ECHO` and
`ICANON` from the PTY. It is disabled for password prompts, unknown terminal
state, control characters, and raw/full-screen applications. A control input
also suspends subsequent prediction until the server explicitly synchronizes
the PTY mode again. This covers silent password reads such as `read -s` while
reducing ordinary command-line typing latency.

Residual risk remains: an authorized administrator can intentionally damage
application data, execute resource-intensive commands, or start processes that
outlive the browser session. Keep the terminal disabled when it is not needed,
and prefer ordinary SSH with OS-level keys, MFA/bastion policy, and a dedicated
administrative account for routine server administration.
