# Changelog

All notable changes to JEKYO are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/), and JEKYO follows
[Semantic Versioning](https://semver.org/): while pre-1.0, minor versions
may contain breaking changes and patches never do. From 1.0 on, breaking
changes only land in major versions.

## [0.15.0] - 2026-08-01

### Added

- `jekyo update`: self-update to the latest release in place, atomic
  replace of the running binary (suggests sudo when the install
  location needs it).
- A JEKYO brand box opens the `jekyo ui` header: wordmark, version, an
  update-available alert pointing at `jekyo update`, and the motto.

### Fixed

- The context name no longer repeats in the cpu box border when the
  node is named after the context.

## [0.14.0] - 2026-08-01

### Added

- `jekyo server harden`: installs and configures fail2ban (sshd jail,
  1h bans) and unattended security updates on the server. One command,
  idempotent.
- Backup freshness: the `jekyo ui` server box reports whether every
  volume with a `backup:` schedule has a recent successful snapshot,
  red when overdue; `jekyo top --json` gains a `backups` array with
  per-volume age and overdue flags.
- Update awareness: the ui shows when a newer JEKYO release exists.

### Changed

- The security signals moved into their own bordered box under the
  server box.

## [0.13.0] - 2026-08-01

### Added

- Server security signals in the `jekyo ui` server box: pending system
  updates with the security count highlighted, a red reboot-required
  alert after kernel updates, and failed SSH login attempts from the
  auth log. All read over SSH, nothing installed on the server.

## [0.12.0] - 2026-08-01

### Added

- `http.redirect`: a service with only `http: {domain, redirect}` is a
  pure routing rule. The ingress answers with a permanent redirect and
  the domain keeps automatic TLS; no container runs. www-to-apex
  redirects no longer need an inline Caddy image.
- The `jekyo ui` header gains a server box: apps, pods with a health
  verdict, services, hosted domains, cluster domain, Kubernetes and
  JEKYO versions. Vitals (temperature, load, API latency, certificate
  runway) moved to the footer's right side.
- `jekyo top --json` gains `services` and `domains` counts.

### Changed

- Meters render as slim half-height bars; filled gauges no longer merge
  into blocks across adjacent lines.

## [0.11.1] - 2026-08-01

### Fixed

- Breathing room between every box in `jekyo ui`: header columns, the
  metrics strip, and the sidebar no longer share flush borders.

## [0.11.0] - 2026-08-01

### Added

- Platform vitals in the `jekyo ui` header: Kubernetes API latency and
  days until the nearest TLS certificate expires (red under 14 days)
  next to the cpu summary, disk read/write rates in the disks box
  border, and a swap row in the mem box when swap exists.
- `jekyo top --json` gains `apiLatencyMs` and `nearestCertExpiryDays`,
  so agents can watch cluster health and certificate runway.

## [0.10.1] - 2026-08-01

### Fixed

- The net box no longer stretches: a gpu box (live nvidia stats, or
  "no GPU available") shares its column, and the header bottom edge
  stays flush.
- Memory rows where high is healthy (Available, Cached, Free) use a
  calm meter instead of alarm colors; only Used keeps thresholds.

## [0.10.0] - 2026-08-01

### Changed

- `jekyo ui` layout: service metrics are always visible now. A btop
  strip (cpu, mem, traffic with braille history and now/peak) sits
  above the logs pane for whatever is selected; `m` toggles it, `s`
  swaps logs for status. The metrics tab is gone because it no longer
  needs to exist.
- Contrast pass: labels brightened, selection is amber on dark like
  the JEKYO palette, values in near-white.

## [0.9.0] - 2026-08-01

### Changed

- The `jekyo ui` header now mirrors btop faithfully: fused superscript
  box titles, braille density graphs, a mem box built from
  /proc/meminfo (Total, Used, Available, Cached, Free with meters),
  disks in two-line-per-filesystem layout, and net with rates plus
  lifetime totals.

## [0.8.0] - 2026-08-01

### Added

- Full btop-style server header in `jekyo ui`: every CPU core with its
  own gauge in an adaptive grid, package temperature, load average,
  memory with absolutes, each real filesystem with its own usage bar,
  and network rates. Read over SSH from /proc, no agent on the server.

### Changed

- Tab labels are clean again; the footer legend carries every hotkey.

## [0.7.0] - 2026-08-01

### Added

- `jekyo ui` shows the server's load average and uptime, read over SSH
  per context (skipped gracefully when SSH is unavailable).

### Changed

- Calmer header, btop-style: identity (context, node, pods, uptime)
  lives in the box border, the wordmark moved to the splash only, and
  the two content rows group cpu/mem/load and disk/net.
- Hotkey hints are no longer duplicated: tab keys live on the tabs,
  the footer keeps movement and actions.

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
