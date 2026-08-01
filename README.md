# JEKYO

[![Release](https://img.shields.io/github/v/release/jekyo/jekyo?color=f59e0b&label=release)](https://github.com/jekyo/jekyo/releases/latest)
[![CI](https://github.com/jekyo/jekyo/actions/workflows/ci.yml/badge.svg)](https://github.com/jekyo/jekyo/actions/workflows/ci.yml)
[![License](https://img.shields.io/badge/license-Apache--2.0-8a8478)](LICENSE)

**Stop doing ops.** JEKYO turns any Ubuntu server into your own cloud.
One command installs a batteries-included Kubernetes cluster. One
`jekyo.yaml` describes an app end to end: build, domains, TLS, volumes,
backups, GPUs. Clouds bill per app, per database, per GPU-hour; a server
bills once.

And JEKYO is built to be operated by AI agents. Your DevOps team is
`/jekyo deploy this`.

Website and documentation: [jekyo.com](https://jekyo.com)

## Quickstart

```sh
# 1. Install the CLI (macOS and Linux)
curl -fsSL https://jekyo.com/install | sh

# 2. Turn a server into a cluster (idempotent, preflight-checked)
jekyo server install root@1.2.3.4 --ip 1.2.3.4 \
  --domain example.com --acme-email you@example.com

# 3. Describe your app
jekyo init            # from a template, or write jekyo.yaml by hand

# 4. Ship it
jekyo up              # built, pushed, deployed, routed, TLS'd
jekyo logs myapp -f
```

The installer sets up k3s (pinned version), Envoy ingress (Contour),
automatic TLS certificates (kcert), a private registry reachable from
every namespace, a WireGuard VPN into the cluster network (wg-easy), the
NVIDIA container runtime when a GPU is present, and local-path
persistent volumes. `jekyo server preflight` tells you what will happen
before anything does, and `jekyo server uninstall` returns the machine
to clean.

## One file per app

The DSL is compose-inspired but Kubernetes-honest. Everything an app
needs lives in one `jekyo.yaml`:

```yaml
app: acme

services:
  api:
    build:
      context: .              # or image: ..., or an inline Dockerfile
    port: 8080
    http:
      domain: api.acme.com    # ingress + automatic TLS certificate
    env:
      DATABASE_URL: postgres://acme:${DB_PASSWORD}@db:5432/acme

  db:
    image: pgvector/pgvector:pg16
    port: 5432
    env:
      POSTGRES_PASSWORD: ${DB_PASSWORD}   # secrets stay in .env
    volumes:
      pgdata: /var/lib/postgresql/data    # implies a StatefulSet

volumes:
  pgdata:
    size: 10Gi
    backup:
      schedule: "0 3 * * *"   # nightly restic snapshots to S3
      keep: 7
```

- `gpu: 1` schedules a service on NVIDIA hardware.
- `schedule: "0 3 * * *"` turns a service into a CronJob.
- `health:` adds HTTP or exec probes, `resources:` sets guarantees and caps.
- Builds are content-hashed: unchanged sources never rebuild, and images
  are built for the server's architecture and delivered over SSH. No CI
  pipeline, no image hosting account.
- `jekyo render` prints the exact Kubernetes YAML an app compiles to.

The full reference lives at
[jekyo.com/docs/jekyo-yaml](https://jekyo.com/docs/jekyo-yaml), or run
`jekyo schema` for the JSON Schema.

## Templates

300 ready-to-deploy templates (databases, analytics, automation, media,
AI tooling) with an online catalog at
[jekyo.com/templates](https://jekyo.com/templates):

```sh
jekyo templates                 # list the catalog
jekyo init n8n                  # interactive: prompts for domains, secrets, sizes
jekyo init n8n --set DOMAIN=n8n.acme.com --defaults   # automation-friendly
jekyo up
```

Templates declare their inputs. Humans get prompts, agents get
`jekyo templates inspect <name> -o json`. Generated secrets land in
`.env`, never in `jekyo.yaml`.

## Backups

Volumes back up to any S3-compatible storage (MinIO, Backblaze, Hetzner,
AWS) as restic snapshots: incremental, deduplicated, encrypted.

```sh
jekyo backup config --endpoint s3.eu-central-1.wasabisys.com \
  --bucket acme-backups --access-key ... --secret-key ...   # once per cluster
jekyo backup now acme/pgdata
jekyo backup ls acme/pgdata
jekyo backup restore acme/pgdata          # stops the app, restores, restarts
```

Scheduled backups come from the `backup:` block in `jekyo.yaml`; restores
are one command and survive a dead server, as long as the bucket lives.

## Day-2 operations

| Command | What it does |
| --- | --- |
| `jekyo ls` / `jekyo ps` | Deployed apps, their pods and state |
| `jekyo logs <app> -f` | Stream logs for an app or one service |
| `jekyo exec <app>/<svc>` | Shell into a running service |
| `jekyo status <app>` | Rollout state, endpoints, certificates, events |
| `jekyo restart <app>[/<svc>]` | Rolling restart |
| `jekyo history` / `jekyo rollback` | Release revisions; re-apply any of them |
| `jekyo down <app>` | Remove workloads (volumes survive without `--volumes`) |
| `jekyo vpn add-peer` | WireGuard config for private access to cluster services |
| `jekyo registry login` | Pull from external private registries |
| `jekyo context export \| import` | Move a server credential to CI or another machine |
| `jekyo kubectl -- ...` | Full kubectl against the cluster when you need it |

## AI agents

```sh
jekyo skill install --global
```

teaches Claude Code, Codex, and Cursor the complete DSL and CLI: then
`/jekyo deploy this` in any session does the rest. Any other agent can
run `jekyo skill show` for the same reference as plain text. There is no
separate agent API; the CLI is the API, and everything an agent needs
(structured output, `-o json`, non-interactive flags) is built into the
commands themselves.

Agents can also migrate existing servers: the skill includes a
docker-compose conversion guide, and the CLI was validated by migrating
a production compose server (data included) in one session.

## How it works

- **SSH is the only requirement.** No agent daemon on the server. The CLI
  connects with your key or ssh-agent, installs k3s, and talks to the
  Kubernetes API over the same channel.
- **Convergent installs.** `server install` is idempotent: rerunning it
  upgrades configuration in place; preflight refuses to break a machine
  it does not understand.
- **Real Kubernetes underneath.** Apps compile to plain
  Deployments/StatefulSets/CronJobs applied server-side with pruning, so
  `kubectl` always tells the truth and nothing is hidden in a database.
- **Per-app namespaces** (`jekyo-<app>`) keep apps isolated and pave the
  way for multi-tenant quotas.

See [SPEC.md](SPEC.md) for the full specification and
[CHANGELOG.md](CHANGELOG.md) for releases (semantic versioning;
pre-1.0 minor versions may break).

## Development

```sh
make build   # build ./jekyo
make test
```

Contributions welcome. Conventional commits, `go test ./...` green, and
for anything user-visible a line in the changelog.

## License

Apache-2.0.
