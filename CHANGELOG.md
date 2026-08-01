# Changelog

All notable changes to JEKYO are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/), and JEKYO follows
[Semantic Versioning](https://semver.org/): while pre-1.0, minor versions
may contain breaking changes and patches never do. From 1.0 on, breaking
changes only land in major versions.

## [0.2.0] - 2026-08-01

### Added

- Volume backups: `backup:` per volume compiles to scheduled restic
  snapshots on any S3-compatible storage; `jekyo backup config`, `now`,
  `ls`, and `restore` (restore stops the app, replaces the volume,
  restarts).
- Template inputs: templates declare domains, secrets, and sizes;
  `jekyo init` prompts for them, `--set`/`--defaults` resolve them
  non-interactively, `jekyo templates inspect -o json` exposes them to
  agents. Secrets land in `.env`, never in jekyo.yaml.
- Template catalog: 300 validated templates at github.com/jekyo/templates,
  browsable on the website and via `jekyo templates`.
- Backup-aware `jekyo init`: offers a schedule (15m, hourly, daily,
  weekly, or cron) for every volume a template ships; `--backup` for
  automation.
- Scheduled jobs: `schedule:` on a service compiles to a CronJob.
- Shared volumes: several services mount one volume via `subpath`.
- Exec health probes: `health.command` alongside HTTP probes.
- External registries: `jekyo registry login` wires pull secrets
  automatically.
- `jekyo context export`/`import` for CI and second machines.
- One-line installer script (`curl -fsSL https://jekyo.app/install | sh`).
- `--internal-domain` install flag for a custom cluster DNS suffix.
- Agent skill v2: complete DSL and CLI surface plus a docker-compose
  migration guide; `jekyo skill install --global` works in every session.

### Changed

- App namespaces are now `jekyo-<app>` (grouped and recognizable in
  kubectl). Apps deployed before 0.2.0 keep working; they adopt the new
  namespace on their next `jekyo up`.
- Command output goes to stdout, so pipes and scripts see it.

### Fixed

- Backup secret provisioning on an app's first deploy.
- `jekyo render` works on apps with `build:` services.

## [0.1.0] - 2026-08-01

Initial release.

- `jekyo server install`: idempotent preflight-gated installer turning an
  Ubuntu server into a k3s cluster with Envoy ingress (Contour), automatic
  TLS (kcert), a private registry, a WireGuard VPN (wg-easy), and NVIDIA
  GPU support.
- The jekyo.yaml DSL compiled to Kubernetes with server-side apply,
  pruning, and release records.
- Content-hashed builds delivered over SSH, targeting the server's
  architecture.
- Day-2 commands: ls, ps, logs, exec, status, restart, history, rollback,
  down.
- AI agent integration: skill packs for Claude Code, Codex, and Cursor.
