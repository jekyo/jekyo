# JEKYO: writing and operating jekyo.yaml apps

JEKYO deploys apps described by a single `jekyo.yaml` to a k3s cluster.
This reference is complete: keys not listed here do not exist. Do not
invent keys; unknown keys are hard errors.

## File shape

```yaml
app: myapp              # required; lowercase alphanumeric/dashes; namespace becomes jekyo-<name>
description: One-liner  # optional metadata (catalogs, dashboards)
icon: https://.../logo.svg   # optional metadata

services:               # required, at least one
  <name>:
    # exactly one of:
    image: registry/repo:tag
    build:
      context: .              # build context dir (relative to jekyo.yaml)
      dockerfile: Dockerfile  # path, OR:
      inline: |               # literal Dockerfile contents
        FROM alpine
      args:                   # build args
        KEY: value

    command: ["binary"]       # container entrypoint override
    args: ["--flag"]          # container args
    env:
      KEY: value
      SECRET: ${VAR}          # interpolated from environment / --env-file; unset = error
      LITERAL: costs $$5      # $$ escapes a literal $
    port: 8080                # main port (sugar for one-element ports:)
    ports: [8080, 9090]       # several ports
    http:                     # publish on a domain via ingress
      domain: api.example.com # required inside http
      path: /                 # default /
      tls: true               # default true; automatic certificate
      redirect: example.com   # domain-level redirect answered by the ingress;
                              # a redirect service defines NOTHING else (no
                              # image/build/port), it runs no container
    expose:                   # raw TCP/UDP on the node
      - port: 5432
        node: 30432           # NodePort 30000-32767
        protocol: tcp         # tcp|udp
    resources:
      cpu: 500m               # guaranteed (requests)
      memory: 512Mi
      cpu-max: "1"            # hard caps (limits); unset = uncapped
      memory-max: 1Gi
    replicas: 2               # default 1; >1 not allowed with volumes
    stateful: true            # force StatefulSet; implied by volumes:
    health:
      path: /healthz          # HTTP readiness+liveness probe, OR:
      command: ["mysqladmin", "ping"]   # exec probe (mutually exclusive with path)
      port: 8080              # default: main port
    gpu: 1                    # NVIDIA runtime; or {count: 1, devices: "0,2"}
    schedule: "0 3 * * *"     # makes this a CronJob; excludes http/replicas/volumes/expose
    volumes:
      data: /var/lib/data     # volume name -> mount path, OR share one volume:
      shared:
        path: /var/lib/app
        subpath: app          # several services share one volume via subpaths

volumes:                # required for every volume mounted above
  data:
    size: 10Gi          # required
    class: local-path   # optional storage class
    backup:             # optional scheduled backups (restic to S3)
      schedule: "0 3 * * *"   # cron; jekyo init also accepts 15m/hourly/daily
      keep: 7                 # snapshots retained

inputs:                 # TEMPLATES ONLY; resolved and stripped by jekyo init
  DOMAIN:
    kind: domain        # domain | secret | string | size
    prompt: Your domain
```

## Semantics to rely on

- Services reach each other by service name (`db:5432`) within the app.
- `volumes:` on a service implies a StatefulSet; data survives `jekyo down`
  (only `jekyo down --volumes` deletes it).
- A volume mounted by several services (via subpath) becomes one shared
  claim with one size.
- `http:` gives the service a TLS URL automatically on clusters installed
  with a domain.
- Built images are tagged by content hash: `jekyo up` skips unchanged builds.
- Backups need a one-time cluster target: `jekyo backup config --endpoint
  ... --bucket ... --access-key ... --secret-key ...` for S3-compatible
  storage, or `jekyo backup config --local` to store repositories on the
  server at /var/lib/jekyo/backups (mount a dedicated disk there). Then
  `jekyo backup now|ls|restore <app>/<volume>`. Restore stops the app,
  replaces the volume, and restarts it.
- External private registry images need `jekyo registry login <host>` once
  per context; pull secrets are wired automatically.

## Migrating from docker-compose

Convert a compose file to jekyo.yaml with these rules:

| compose | jekyo.yaml |
| --- | --- |
| `services.<n>.image` | `services.<n>.image` unchanged |
| `build:` | `build:` (context/dockerfile/args map directly) |
| `environment:` (list or map) | `env:` map; quote all values |
| `ports: ["8080:80"]` | `port: 80` (container port); public access via `http:` instead |
| `volumes: [data:/var/lib/x]` | service `volumes:` + top-level `volumes:` with a size |
| `volumes: [./file.conf:/etc/x]` | not supported; bake the file into the image with `build.inline` |
| `depends_on`, `restart`, `container_name`, `networks`, `labels` | drop them; Kubernetes handles these |
| `healthcheck.test: [CMD, x]` | `health.command: [x]` |
| `healthcheck.test: [CMD-SHELL, x]` | `health.command: [sh, -c, x]` |
| exposed web service | add `http: {domain: ...}` to it |
| www/apex redirect rules (nginx/caddy blocks) | a service with only `http: {domain, redirect}` |
| secrets in env | move values to `.env`, reference as `${VAR}` |

Compact multiple data volumes into one volume with subpaths and one size.
Always run `jekyo render` on the result before `jekyo up`.

Migrating data into a fresh volume: deploy the app, then stream the old
files into the running pod:

```
tar -C /old/data -cf - . | jekyo kubectl -- exec -i -n jekyo-<app> <pod> -- tar -xf - -C /data
jekyo restart <app>/<service>
```

For large or database-backed apps, prefer the application's own dump and
restore tools (pg_dump, mysqldump, redis SAVE) over raw file copies.

## CLI you should use

```
jekyo render [-f file]         # ALWAYS check generated Kubernetes YAML before deploying
jekyo up [-f file] [--env-file .env]
jekyo ls | ps [app] | status <app>       # -o json on list commands
jekyo logs <app>[/<service>] [-f] [-t] [--since 1h] [--tail N]   # -t prefixes timestamps
jekyo exec <app>/<service> -- <cmd>
jekyo attach <app>/<service>   # stream the main process output; Ctrl+C detaches
jekyo top [app] --json         # snapshot: per-pod cpu/mem/network/restarts, node cpu/mem/disk, volume usage; ALWAYS pass --json (network counters are cumulative; diff two snapshots for rates)
jekyo ui                       # interactive TUI for humans; agents must NOT run this (use the --json commands)
jekyo restart <app>[/<service>]
jekyo history <app> | rollback <app> [rev]
jekyo down [app] [--volumes]

jekyo templates [query] | templates inspect <name> [-o json]   # catalog; query fuzzy-searches name+description
jekyo init <name> [--defaults] [--set NAME=value] [--backup daily]
jekyo backup config|now|ls|restore
jekyo registry login <host>
jekyo images | build [-f file]
jekyo vpn peers | add-peer | rm-peer | config
jekyo server harden            # fail2ban + automatic security updates on the server
jekyo context ls | use | show | export | import
jekyo schema                   # JSON Schema for validation
jekyo update                   # self-update the CLI to the latest release
```

Validation is strict: run `jekyo render` after editing jekyo.yaml: parse
or validation errors point at the exact problem. Never hand-edit generated
Kubernetes resources; change jekyo.yaml and re-run `jekyo up`.
