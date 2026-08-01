# JEKYO: writing and operating jekyo.yaml apps

JEKYO deploys apps described by a single `jekyo.yaml` to a k3s cluster.
This reference is complete: keys not listed here do not exist. Do not
invent keys; unknown keys are hard errors.

## File shape

```yaml
app: myapp              # required; lowercase alphanumeric/dashes; namespace becomes jekyo-<name>
description: One-liner  # optional metadata
icon: https://.../logo.png   # optional metadata

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
      path: /healthz          # readiness+liveness HTTP probes
      port: 8080              # default: main port
    gpu: 1                    # NVIDIA runtime; or {count: 1, devices: "0,2"}
    schedule: "0 3 * * *"     # makes this a CronJob; excludes http/replicas/volumes/expose
    volumes:
      data: /var/lib/data     # volume name -> mount path

volumes:                # required for every volume mounted above
  data:
    size: 10Gi          # required
    class: local-path   # optional storage class
    backup:             # optional scheduled backups (restic to S3)
      schedule: "0 3 * * *"   # cron; jekyo init also accepts 15m/hourly/daily
      keep: 7                 # snapshots retained
```

## Semantics to rely on

- Services reach each other by service name (`db:5432`) within the app.
- `volumes:` on a service implies a StatefulSet; data survives `jekyo down`
  (only `jekyo down --volumes` deletes it).
- `http:` gives the service a TLS URL automatically on clusters installed
  with a domain.
- Built images are tagged by content hash: `jekyo up` skips unchanged builds.
- Backups need a one-time cluster target: `jekyo backup config --endpoint ... --bucket ... --access-key ... --secret-key ...`. Then `jekyo backup now|ls|restore <app>/<volume>`.
- External private registry images need `jekyo registry login <host>` once
  per context; pull secrets are wired automatically.

## CLI you should use

```
jekyo render -f jekyo.yaml     # ALWAYS check generated Kubernetes YAML before deploying
jekyo up [-f file] [--env-file .env]
jekyo ls | ps [app] | status <app>
jekyo logs <app>[/<service>] [-f] [--since 1h] [--tail N]
jekyo exec <app>/<service> -- <cmd>
jekyo restart <app>[/<service>]
jekyo history <app> | rollback <app> [rev]
jekyo down [app] [--volumes]
jekyo images | build [-f file]
jekyo schema                   # JSON Schema for validation
```

Validation is strict: run `jekyo render` after editing jekyo.yaml: parse
or validation errors point at the exact problem. Never hand-edit generated
Kubernetes resources; change jekyo.yaml and re-run `jekyo up`.
