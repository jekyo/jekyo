# Changelog

All notable changes to JEKYO are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/), and JEKYO follows
[Semantic Versioning](https://semver.org/): while pre-1.0, minor versions
may contain breaking changes and patches never do. From 1.0 on, breaking
changes only land in major versions.

## [0.6.3] - 2026-08-01

### Fixed

- Sidebar columns are labeled (SERVICE, CPU, MEM) so the numbers read
  at a glance.

## [0.6.2] - 2026-08-01

### Added

- `jekyo ui` opens with the JEKYO logomark splash while the first
  snapshot loads.

### Fixed

- The selected service in the sidebar is unmissable now: a pointer and
  a full-width highlight band instead of a subtle recolor.
- Inactive tabs show their jump key (logs l, metrics m, status s).

## [0.6.1] - 2026-08-01

### Fixed

- `jekyo ui` icons render in every terminal font. The default set is
  now plain Unicode; Nerd Font glyphs (which showed as ? without a
  patched font) moved behind `--nerd` / `JEKYO_NERD=1`.

## [0.6.0] - 2026-08-01

### Added

- `jekyo ui` visual overhaul: the JEKYO wordmark and a boxed server
  strip in the header, Nerd Font icons throughout (pass `--ascii` or
  set `JEKYO_ASCII=1` on terminals without a patched font), a proper
  tab bar, keycap-style footer, full-width row selection, and clearer
  confirm and status banners.

## [0.5.0] - 2026-08-01

### Added

- Traffic and disk everywhere: `jekyo ui` grew btop-style server gauges
  (CPU, memory, disk) with history sparklines, live network rates for
  the whole server, per-service download/upload sparklines in the
  metrics tab, and volume usage bars in the status tab.
- `jekyo top --json` now includes network counters per pod and node,
  node disk usage, and per-volume usage, all from the kubelet stats
  API. Diff two snapshots for rates.

### Changed

- `jekyo top` and `jekyo ui` merged into one dashboard: `top` in a
  terminal opens the UI; piped or with `--json` it prints the snapshot.
- `jekyo --help` is organized into groups (Apps, Observe, Operate,
  Servers & access, Agents & tooling) so the everyday commands stand
  out and plumbing sinks to the bottom.

## [0.4.0] - 2026-08-01

### Added

- `jekyo ui`: an interactive terminal UI in the spirit of lazydocker and
  btop combined. Left pane navigates apps and services with live status
  and resource use; right pane tabs between streaming logs, CPU and
  memory history graphs, and status with recent events; node capacity
  gauges in the header. One key acts: `r` restart (with confirm),
  `b` rollback, `e` exec, `a` attach, `q` quit.

## [0.3.0] - 2026-08-01

### Added

- `jekyo top`: a live btop-style resource dashboard. Per-pod CPU, memory,
  restarts, and status with usage bars against limits, plus node capacity
  gauges. `jekyo top --json` prints one machine-readable snapshot for
  agents and scripts.
- `jekyo attach`: stream a service's main process output live;
  Ctrl+C detaches without stopping anything. `-i` forwards stdin.
- `jekyo logs -t/--timestamps` prefixes each line with its RFC3339
  timestamp.

## [0.2.2] - 2026-08-01

### Changed

- JEKYO's home is jekyo.com: the one-line installer is
  `curl -fsSL https://jekyo.com/install | sh`, and all documentation
  links point there.

## [0.2.1] - 2026-08-01

Fixes found while validating JEKYO on real hardware with a public domain,
migrating a production docker-compose server.

### Fixed

- Pods in app namespaces can now pull images pushed to the private
  registry. containerd matches registry credentials against the endpoint
  host after the mirror rewrite, so auth is keyed by both
  `registry.jekyo.local` and the in-cluster address.
- `jekyo server install` restarts k3s when a converge changes the
  registry configuration; containerd only reads it at startup.
- Automatic TLS certificates are actually issued now. kcert ran with two
  wrong defaults: it looked for itself in the `kcert` namespace (RBAC
  denied) and created ACME challenge ingresses with class `nginx`, which
  Envoy ignores. First real Let's Encrypt issuance verified end to end.

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
- One-line installer script (`curl -fsSL https://jekyo.com/install | sh`).
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
