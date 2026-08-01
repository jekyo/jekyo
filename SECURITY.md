# Security Policy

JEKYO installs software on servers and handles credentials (SSH keys,
registry passwords, VPN configs, backup keys). We take reports in this
area seriously.

## Supported versions

Pre-1.0, only the latest release receives security fixes. Update with
the same command that installed the CLI:

```sh
curl -fsSL https://jekyo.com/install | sh
```

## Reporting a vulnerability

Report vulnerabilities privately via
[GitHub security advisories](https://github.com/jekyo/jekyo/security/advisories/new),
or by email to 0x4139@gmail.com with `[SECURITY]` in the subject.

Please do not open public issues for security problems.

Include what you can: affected version (`jekyo version`), a
reproduction, and the impact you believe it has. You will get an
acknowledgment within 72 hours and a status update at least weekly
until the report is resolved. Fixes ship as a patch release with credit
to the reporter in the changelog, unless you prefer to stay anonymous.

## Scope notes

- The CLI connects to servers you own over SSH; it never phones home.
- Cluster credentials live in `~/.jekyo` on your machine. Anyone with
  that directory can administer your servers; treat it like an SSH key.
- Generated secrets (registry, VPN, backups) are stored as Kubernetes
  Secrets on your own cluster.
- The one-line installer downloads release binaries from GitHub over
  HTTPS, and `jekyo update` additionally verifies the release's sha256
  checksums. Reports about the integrity of that path are in scope.
- Known limitation: during image delivery the registry credential
  briefly appears in a process argument on your own server (visible
  only to users already on that machine). Treat shell access to the
  server as equivalent to registry access.
