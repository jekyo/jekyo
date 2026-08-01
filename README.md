# JEKYO

A personal PaaS on k3s: one command turns a bare Ubuntu server into a
batteries-included cluster, and one `jekyo.yaml` file describes an app —
build, runtime, routing, volumes — end to end.

```sh
jekyo server install root@1.2.3.4 --ip 1.2.3.4 --storage /storage \
  --domain example.com --acme-email you@example.com

jekyo init            # or write jekyo.yaml by hand
jekyo up              # built, pushed, deployed, routed, TLS'd
jekyo logs myapp -f
```

**What the installer sets up:** k3s (pinned), Envoy ingress (Contour),
automatic TLS (kcert), a private registry in `kube-system` reachable from
every namespace, a WireGuard VPN into the cluster network (wg-easy), the
NVIDIA container runtime when a GPU is present, and local-path persistent
volumes. Preflight checks run first (`jekyo server preflight`), the install
is idempotent, and `jekyo server uninstall` returns the machine to clean.

**The DSL** is compose-inspired but Kubernetes-honest:

```yaml
app: acme
services:
  api:
    build:
      context: .
    port: 8080
    http:
      domain: api.acme.com     # ingress + automatic TLS
  db:
    image: pgvector/pgvector:pg16
    port: 5432
    volumes:
      pgdata: /var/lib/postgresql/data   # implies StatefulSet
volumes:
  pgdata:
    size: 10Gi
```

`gpu: 1` schedules on NVIDIA. `schedule: "0 3 * * *"` makes a CronJob.
Builds are content-hashed (unchanged sources never rebuild) and target the
server's architecture. `jekyo render` prints the exact Kubernetes YAML.

**Day-2:** `ls`, `ps`, `logs -f`, `exec`, `status`, `restart`,
`history`/`rollback`, `down` (volumes survive unless `--volumes`),
`vpn add-peer`, `registry login` for external private images,
`context export|import` for [deploying from CI](docs/deploy-from-ci.md).

**AI agents:** `jekyo skill install --global` teaches Claude Code / Codex /
Cursor the DSL and CLI (then just say `/jekyo deploy this`). Any other
agent: have it run `jekyo skill show`. `jekyo schema` emits JSON Schema.

See [SPEC.md](SPEC.md) for the full specification. Status: pre-release —
M0–M4 (installer, DSL, builds, day-2 UX) implemented and e2e-tested; M5
(read-only dashboard) in progress.

## Development

```sh
make build   # build ./jekyo
make test
```
