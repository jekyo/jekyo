# JEKYO — Specification

**Status:** draft v0.1 · 2026-07-31
**License:** open source (Apache-2.0 suggested)
**Language:** Go, single static binary

JEKYO turns a bare Ubuntu server with SSH access into a personal PaaS: a k3s
cluster with batteries included (ingress, TLS, registry, VPN, GPU), driven by a
single `jekyo.yaml` per app and a CLI with Docker-grade UX.

```
jekyo server install root@1.2.3.4     # bare server -> ready cluster (one command)
jekyo up                              # jekyo.yaml -> built, pushed, deployed, routed, TLS'd
jekyo ls                              # apps across the cluster
jekyo logs api -f                     # docker-style ergonomics
```

Why Kubernetes (k3s) and not a plain Docker daemon: namespace isolation and
resource quotas for future per-client tenancy, real ingress/TLS, declarative
state, GPU scheduling, and a path to multi-node — while k3s keeps the single-
server footprint close to dockerd's.

---

## 1. Decisions (locked)

| Topic | Decision |
|---|---|
| Runtime | k3s (embedded containerd). No Docker daemon on the server. |
| Ingress & TLS | **Contour** (Envoy data plane, standard Ingress API) + **kcert** (single-pod ACME). Replaces the retired ingress-nginx and heavyweight cert-manager from earlier setups. |
| Builds (v1) | **Local Docker/buildx** on the operator's machine, `--platform linux/amd64`, pushed to the in-cluster registry. In-cluster BuildKit is a v2 option. |
| Architecture | **Pure CLI.** No in-cluster controller. App state stored helm-style in the cluster (Secrets + labels), so any machine with the context sees the same truth. |
| DSL | **Compose-inspired, clean break.** Familiar keys, no compatibility promise. One file holds build + runtime + ingress + volumes. |
| Tenancy | v2. v1 lays the groundwork: one namespace per app, optional resource limits. |
| OS support | Ubuntu 20.04/22.04/24.04, amd64. Single node in v1; `server join` (agents) reserved for v2. |

---

## 2. Component 1 — The installer (`jekyo server`)

### 2.1 Inputs

```
jekyo server install <ssh-target> \
  --ip <public-ip>            # required: advertise address & kubeconfig endpoint
  --storage <path>            # required: local-path provisioner root (e.g. /storage)
  --domain <base-domain>      # optional: enables registry.<domain>, vpn.<domain>
  --acme-email <email>        # required if --domain: Let's Encrypt registration
  --name <context-name>       # default: derived from host
  --internal-domain <suffix>  # cluster DNS suffix (default cluster.local; e.g. jekyo.internal)
  --no-vpn --no-gpu --no-registry   # opt-outs
  --fix                       # auto-remediate fixable preflight warnings (2.3)
  --remove-docker             # purge a detected Docker engine (never implicit)
```

SSH target accepts `user@host[:port]`; auth via ssh-agent or `--ssh-key`.
Everything else is derived or defaulted. The command is **idempotent** — safe to
re-run to converge/repair a cluster.

### 2.2 What it does (over SSH)

1. **Preflight** — full check suite, see 2.3. Also runnable standalone as
   `jekyo server preflight <ssh>` (read-only dry run, no changes).
2. **k3s install** — pinned version (flag `--k3s-version`, default embedded):

   ```
   curl get.k3s.io | INSTALL_K3S_VERSION=vX sh -s - \
     --disable=traefik \
     --advertise-address <ip> \
     --node-external-ip <ip> \
     --default-local-storage-path <storage> \
     --kubelet-arg=allowed-unsafe-sysctls=net.*     # wg-easy needs sysctls
   ```

   The cluster DNS suffix defaults to `cluster.local` and is configurable
   via `--internal-domain` (e.g. `jekyo.internal` for pleasant VPN
   hostnames). The default remains the safe choice when hand-deploying
   third-party manifests, some of which hardcode cluster.local; JEKYO's
   own manifests never do.
3. **GPU (auto-detected, or `--no-gpu`)** — if `lspci` shows NVIDIA: install
   ubuntu-drivers (or accept preinstalled), install `nvidia-container-toolkit`
   from NVIDIA's apt repo, restart k3s (it auto-detects the runtime and wires
   containerd), apply `RuntimeClass nvidia`, then run a CUDA smoke-test pod.
4. **Core addons** — applied as **embedded, pinned, pre-rendered manifests**
   (`go:embed`; no helm on server or in the binary):
   - **Contour** (Envoy data plane) — default IngressClass, Envoy on
     hostPorts 80/443. Serves standard Ingress plus Contour's HTTPProxy CRD
     (used where features exceed Ingress, e.g. basic auth via
     `contour-authserver`, installed on demand). Chosen over the retired
     ingress-nginx (archived March 2026).
   - **kcert** — single-pod ACME (HTTP-01 via Contour) when `--domain` is
     set; issues/renews TLS Secrets per Ingress. Self-signed fallback
     otherwise. Chosen over cert-manager for footprint (one pod, no CRDs);
     no DNS-01/wildcards, which is fine while certs are per-domain. The
     DSL compiler goes through a `CertProvider` seam that only decides
     which annotation lands on the Ingress, so falling back to
     cert-manager (same TLS-Secret contract) stays a contained swap.
   - **registry** — `registry:2` + UI, StatefulSet with PVC on local-path.
     - Reachable **in-cluster from any namespace**: k3s `registries.yaml`
       maps `registry.jekyo.local` → the registry Service, so any pod spec
       can say `image: registry.jekyo.local/myapp/api:tag` with no per-
       namespace pull secrets. (When v2 adds agent nodes, the installer
       distributes the same `registries.yaml` to each node — image
       references never change.)
     - Reachable **from the laptop** (needed for local builds): Ingress at
       `registry.<domain>` with TLS + htpasswd basic auth (credentials
       generated at install, stored in the context). Without `--domain`,
       push goes through a port-forward opened automatically by the CLI.
   - **wg-easy VPN** (skippable) — StatefulSet, privileged, NodePort UDP
     31820, admin UI behind ingress. Generated admin password. Pushes
     CoreDNS (10.43.0.10) as client DNS so `*.svc.cluster.local` resolves
     from the laptop. This is how you reach ClusterIP services directly.
     Day-to-day peer management goes through `jekyo vpn` (wraps wg-easy's
     REST API), not the UI — so if wg-easy is ever replaced with plain
     wireguard-tools, the CLI surface survives.
5. **Kubeconfig** — pull `/etc/rancher/k3s/k3s.yaml`, rewrite `127.0.0.1` →
   public IP, store under `~/.jekyo/contexts/<name>/`, set as current context.
6. **Report** — print endpoints, credentials (registry, VPN admin), and next
   steps. Also written to the context dir.

### 2.3 Preflight

Every check reports **PASS / WARN / FAIL** and prints as a table before any
change is made. FAILs abort the install; WARNs proceed but are listed in the
final report. Checks marked *fixable* are remediated automatically when
`--fix` is passed (each fix logged); destructive remediations additionally
require their own explicit flag — nothing is ever removed implicitly.

| Check | Severity | Remediation |
|---|---|---|
| Ubuntu 20.04/22.04/24.04, amd64, root/sudo | FAIL | — |
| Disk space at storage path, RAM minimum | FAIL | — |
| Ports 80/443/6443 (TCP), 31820 (UDP) free | FAIL | points at the owning process; if it's a Docker container, see below |
| Other k8s present (kubeadm residue in `/etc/kubernetes`, microk8s, minikube) | FAIL | manual — too risky to auto-remove |
| Existing k3s | WARN | converge (idempotent re-install) or `--force-reinstall` |
| **Docker installed** | WARN | k3s doesn't need it gone (separate containerd), but it wastes resources and its containers can squat ports. `--remove-docker` purges engine+containers; reclaiming `/var/lib/docker` asks a separate confirmation (data loss). Without the flag: warn, and FAIL only if a Docker container holds a required port. |
| ufw / firewalld active | WARN, *fixable* | `--fix` adds rules: 80/443/6443 in, 31820/udp in, allow pod/service CIDRs (10.42.0.0/16, 10.43.0.0/16) |
| NetworkManager managing all interfaces | WARN, *fixable* | `--fix` drops config ignoring `cni*`/`flannel*`/`veth*` |
| Clock not syncing (no timesyncd/chrony) | WARN, *fixable* | `--fix` enables systemd-timesyncd — skewed clocks break ACME issuance |
| `wireguard` kernel module unavailable | WARN | skip VPN addon with notice (implied `--no-vpn`) |
| Swap enabled | WARN | informational — k3s tolerates it, memory limits get fuzzy |
| GPU path: Secure Boot enabled (`mokutil`) | WARN | unsigned NVIDIA DKMS modules won't load; instruct MOK enrollment or disable SB |
| GPU path: nouveau driver bound | WARN, *fixable* | `--fix` blacklists nouveau (takes effect after reboot) |

### 2.4 Uninstall

```
jekyo server uninstall <context> [--purge-storage]
```

Runs `k3s-uninstall.sh` (and agent variant if present) per
https://docs.k3s.io/installation/uninstall, removes the NVIDIA toolkit apt
pins JEKYO added, and with `--purge-storage` (double-confirm) wipes the
storage path. Removes the local context. This is the "repurpose a server"
path: install and uninstall are symmetric.

---

## 3. Component 2 — The DSL (`jekyo.yaml`)

One file, everything an app needs: image or build steps, runtime config,
ports, HTTP routing/TLS, persistent volumes, GPU, resources. The compiler
turns it into Deployments/StatefulSets, Services, Ingresses, PVCs, Secrets —
the ~120 lines of YAML the old `models/*.yaml` files were by hand.

### 3.1 Reference example

```yaml
# jekyo.yaml
app: acme                       # app name -> namespace "acme"

services:
  api:
    build:                      # EITHER build...
      context: .
      dockerfile: Dockerfile    # or `inline: |` for a literal Dockerfile
      args:
        VERSION: "1.4"
    # image: ghcr.io/acme/api:1.4    # ...OR a prebuilt image
    command: ["./server"]           # optional override
    env:
      DB_URL: postgres://db:5432/acme
      API_KEY: ${API_KEY}           # interpolated from local env / --env-file
    port: 8080                      # main port; `ports:` for multiple
    http:                           # ingress in four lines
      domain: api.acme.com
      path: /                       # default
      tls: true                     # default when server has --domain/issuer
      auth: basic                   # optional htpasswd auth at the edge
    resources:
      cpu: 500m                     # guaranteed; add cpu-max / memory-max for hard caps
      memory: 512Mi
    replicas: 2
    health:
      path: /healthz                # -> readiness+liveness probes

  db:
    image: pgvector/pgvector:pg16
    env:
      POSTGRES_PASSWORD: ${PG_PASS}
    port: 5432
    volumes:
      pgdata: /var/lib/postgresql/data
    stateful: true                  # -> StatefulSet (implied by volumes:)

  llm:
    image: vllm/vllm-openai:latest
    args: ["--model=meta-llama/Llama-3.1-8B", "--port=5000"]
    port: 5000
    gpu: 1                          # -> runtimeClass nvidia + device envs
    volumes:
      hf-cache: /root/.cache/huggingface
    http:
      domain: llm.acme.com

volumes:                            # local-path PVCs
  pgdata:
    size: 10Gi
  hf-cache:
    size: 40Gi
```

### 3.2 Semantics

- **`app`** — names the app; its namespace is `jekyo-<app>` (grouped and
  recognizable in kubectl, isolated per app). Every generated resource is
  labeled `jekyo.io/app`, `jekyo.io/service`, `jekyo.io/revision`.
- **`resources`** — flat keys: `cpu` and `memory` are what the service is
  guaranteed (k8s requests); `cpu-max` and `memory-max` are hard caps (k8s
  limits). Unset means unset — no implicit caps. No nested
  requests/limits blocks.
- **`port` / `ports`** — `port: 8080` for the common single-port case;
  `ports:` list when a service exposes several. `port` is pure sugar for a
  one-element `ports:`.
- **`build` vs `image`** — exactly one per service. `build` produces
  `registry.jekyo.local/<app>/<service>:<content-hash>`; the content hash of
  the build context makes `up` skip unchanged builds.
- **Service discovery** — services reach each other by name (`db:5432`),
  which is just the k8s Service DNS name in the shared namespace. No
  `depends_on`: Kubernetes restarts until dependencies come up; `health`
  probes gate readiness. (An optional `wait-for:` sugar can come later.)
- **`stateful: true`** (implied by having `volumes`) — StatefulSet +
  volumeClaimTemplate; otherwise Deployment.
- **`gpu: N`** — sets `runtimeClassName: nvidia`, `NVIDIA_VISIBLE_DEVICES`
  handling, and node scheduling; `gpu: {count: 1, devices: "0,2"}` for
  pinning specific devices (the vllm/exllama pattern).
- **`http`** — compiles to an Ingress with the kcert annotation (TLS Secret
  issued/renewed automatically). `auth: basic` upgrades the route to a
  Contour HTTPProxy wired to contour-authserver with a generated htpasswd
  Secret. `expose: {port: N, node: M}` covers raw TCP/UDP NodePorts (the
  wg-easy pattern).
- **Interpolation** — `${VAR}` from process env and `--env-file`; fails
  loudly on unset vars. Values marked `secret: true` (or under a top-level
  `secrets:` block) land in k8s Secrets, not ConfigMaps.
- **`schedule`** — a service with `schedule: "0 3 * * *"` compiles to a
  CronJob instead of a Deployment (mutually exclusive with `http`,
  `replicas`, `volumes`): image/build + command + env, run on the cron
  spec.
- **`description` / `icon`** — optional app-level metadata: a one-liner and
  a logo URL. Shown by the dashboard and used by the template catalog
  (§4.2). (`icon`, not `image`, to avoid clashing with the service-level
  `image` key.)
- **Unknown top-level `x-*` keys** are ignored (escape hatch), anything else
  unknown is a hard validation error with a did-you-mean suggestion.

### 3.3 Compilation & state

`jekyo up` = parse → validate → build/push changed images → render manifests
→ **server-side apply** (field manager `jekyo`) → prune resources labeled with
the app that are absent from the new render → record a release Secret
(`jekyo.io/release`, numbered revisions with the rendered manifest, like
helm) in the app namespace. `jekyo rollback <app> [rev]` re-applies a prior
revision. `jekyo down <app>` deletes the namespace (volumes survive unless
`--volumes`).

---

## 4. Component 3 — The CLI

Docker-grade UX: contexts are servers, apps are jekyo files.

```
CONTEXTS (servers)
  jekyo context ls | use <name> | rm <name> | show
  jekyo server preflight <ssh>        # read-only check suite (section 2.3)
  jekyo server install <ssh> ...      # creates a context (section 2)
  jekyo server uninstall <context>
  jekyo server info                   # versions, addon health, disk, GPUs

APPS
  jekyo up   [-f jekyo.yaml] [--build] [--env-file .env]
  jekyo down [app] [--volumes]
  jekyo ls                            # apps: name, services, status, domains, age
  jekyo ps   [app]                    # pods: ready, restarts, node, gpu
  jekyo logs <app>[/<service>] [-f] [--since 1h]
  jekyo exec <app>/<service> [--] <cmd>   # default: interactive shell
  jekyo restart <app>[/<service>]
  jekyo status <app>                  # rollout state, endpoints, certs, events
  jekyo rollback <app> [revision]
  jekyo history <app>

BUILD / REGISTRY
  jekyo build [-f jekyo.yaml]         # build+push only (local docker, linux/amd64)
  jekyo images                        # registry catalog (talks to registry API)

ESCAPE HATCHES
  jekyo render [-f jekyo.yaml]        # print generated k8s YAML (debuggability!)
  jekyo kubectl -- <args>             # kubectl against current context
  jekyo vpn config [peer]             # download a WireGuard client config
  jekyo vpn add-peer <name> | rm-peer <name>   # wraps wg-easy's REST API

AI AGENTS
  jekyo skill install [--agent all]   # teach coding agents the DSL (section 4.1)
  jekyo schema                        # print the jekyo.yaml JSON Schema

DASHBOARD (section 5)
  jekyo dashboard [--port 7777]       # serve read-only UI locally, open browser
  jekyo dashboard deploy [--domain d | --vpn-only] | remove
```

### 4.1 Template catalog (`jekyo init`)

```
jekyo templates                 # list the catalog (name, description)
jekyo init postgres             # write a ready-to-edit jekyo.yaml
```

Templates are ordinary jekyo.yaml files with `description:` and `icon:`
metadata, hosted in a public GitHub repo (`jekyo/templates`) with an
`index.yaml` — so the catalog grows and updates without cutting a CLI
release. `jekyo init` fetches from the catalog (raw.githubusercontent) and
falls back to a small embedded set (postgres, redis, static site) when
offline. The dashboard renders the same descriptions/icons.

**Template inputs.** Templates declare what they need in an `inputs:`
block — each input has a `kind` (`domain` | `secret` | `string` | `size`),
an optional `prompt`, `default`, and `required` (default true; secrets
auto-generate when not provided). Two resolution modes:

- *Interactive:* `jekyo init <name>` prompts for each unresolved input.
- *Automated (AI/CI):* `jekyo templates inspect <name> -o json` exposes the
  requirements schema; `jekyo init <name> --set key=value... --defaults`
  resolves non-interactively and fails listing any missing required input.

Resolution substitutes non-secret values (domains, sizes) into the written
jekyo.yaml, generates/collects secrets into a `.env` file (gitignored, the
yaml keeps `${VARS}`), and strips the `inputs:` block. Templates prefer a
single volume per app (services mount subpaths) so one size question
covers storage.

The catalog is seeded by converting Coolify's Apache-2.0 compose templates
(362 services; attribution in the repo): their magic vars map cleanly
(`$SERVICE_PASSWORD_X` → secret input, `$SERVICE_URL_X` → domain input,
`${X:-def}` → optional input with default, `${X}` → required input), a
converter does the bulk, curation makes them good.

### 4.2 Deploy from CI (push-to-deploy)

No server-side git integration — a documented GitHub Actions recipe does
it the pure-CLI way: the workflow installs `jekyo`, restores a context
from a repo secret, and runs `jekyo up`. PR preview environments can come
later as `jekyo up --name-suffix pr-N`.

### 4.3 AI-agent integration (`jekyo skill`)

The strategic bet: JEKYO is infrastructure an LLM can safely operate —
and **the CLI is the agent API**. Agents with shell access (Claude Code,
Codex, OpenCode, Cursor) drive the binary directly; no MCP server wraps
it (deliberate — an MCP layer would duplicate the CLI for no capability
gain; revisit in v2 only if non-shell hosts demand it). What makes the
CLI agent-grade:

- `jekyo skill install --global` installs the pack user-level
  (~/.claude/skills, ~/.codex/AGENTS.md), omarchy-style: `/jekyo <request>`
  then works in every Claude Code session, no per-project setup. The
  project-scoped variant remains for repos that want it committed.
- `jekyo skill show` prints the complete DSL+CLI reference — the
  "llms.txt", versioned inside the binary; any agent can be told to run
  it once and then operate JEKYO correctly.
- `-o json` on read surfaces (`ls`, `ps`, `history`, ...) for structured
  consumption; predictable exit codes; errors on stderr.
- `jekyo render` / `jekyo schema` let agents self-check generated output
  before deploying.

Coding agents (Claude Code, Codex, OpenCode, Cursor) should be able to write
and modify `jekyo.yaml` files and drive the CLI without hallucinating keys.
`jekyo skill install` detects (or takes via `--agent`) the agents configured
in the current project and installs a skill/instruction pack for each:

| Agent | Artifact written |
|---|---|
| Claude Code | `.claude/skills/jekyo/SKILL.md` (+ reference files) |
| Codex | `AGENTS.md` section (created or appended between markers) |
| OpenCode | `AGENTS.md` (same shared section) |
| Cursor | `.cursor/rules/jekyo.mdc` |

The pack content is embedded in the binary and therefore always matches the
installed version. It contains:

- the **DSL reference** — every key, its type, defaults, and semantics;
- **worked examples** (web+db, GPU model server, static site);
- the **CLI cheat-sheet** (`up`, `render`, `logs`, ...), with the guidance
  that agents should run `jekyo render` to check generated output and
  `jekyo up --dry-run` to validate before deploying;
- a pointer to `jekyo schema` for machine validation.

Idempotent and marker-delimited, so re-running after a JEKYO upgrade
refreshes the section in place. `jekyo schema` additionally emits the JSON
Schema, which also powers editor autocomplete (yaml-language-server header).

Conventions: `-o json` on list commands, `--context` flag everywhere,
exit codes meaningful, colors off when piped. `~/.jekyo/` holds contexts
(kubeconfig, registry creds, endpoints); nothing app-related is stored
locally — the cluster is the source of truth.

---

## 5. Component 4 — The dashboard (`jekyo dashboard`)

A read-only web UI for humans — Lens-adjacent but deliberately minimalist,
and app-centric rather than resource-centric: it renders JEKYO's model
(apps, services, domains, releases), not raw k8s objects.

### 5.1 Modes

- **Local (primary):** `jekyo dashboard [--port 7777]` serves on localhost
  and opens the browser. It talks to the k8s API of the current context
  directly; the UI lives inside the `jekyo` binary. Zero install.
- **In-cluster (optional):** `jekyo dashboard deploy [--domain dash.x |
  --vpn-only]` runs the released `ghcr.io/.../jekyo` image with a
  **read-only ServiceAccount** (get/list/watch only — RBAC enforces
  read-only even if the UI had a bug). Exposure is either behind
  basic-auth ingress (`--domain`) or ClusterIP-only, reachable through the
  VPN (`--vpn-only`, the default). `jekyo dashboard remove` deletes it.

### 5.2 Tech & shape

- Go `html/template` + **htmx**, assets `go:embed`-ded — no SPA framework,
  no node toolchain, no separate repo. SSE (htmx `sse` extension) streams
  live pod status, events, and log tails; everything else is plain
  server-rendered fragments.
- **Read-only by construction:** the handler layer has no mutating
  endpoints at all; in-cluster RBAC is the second wall.
- **Embeddable:** every view renders chrome-less with `?embed=1`, so an
  app's status page or a log tail can be iframed into internal tools.

### 5.3 Views

| View | Contents |
|---|---|
| Apps | name, services, pod readiness, domains (linked), cert status, revision, age |
| App detail | per-service: image/build hash, pods (restarts, node, GPU), probes, volumes, env (secrets masked), events, **live log tail** |
| Nodes | cpu/mem/disk, pressure conditions, GPU inventory & allocation |
| Registry | repositories, tags, sizes (registry v2 API) |
| Events | cluster-wide feed, filterable by app |
| Certs | domains, issuer, expiry, renewal state |

## 6. Implementation

### 6.1 Stack

| Concern | Choice |
|---|---|
| CLI | `spf13/cobra` |
| k8s | `k8s.io/client-go` — dynamic client + server-side apply; no helm SDK |
| Addon manifests | pinned upstream YAML, `go:embed`-ded, templated minimally |
| SSH | `golang.org/x/crypto/ssh` (agent + key auth) |
| Builds | shell out to `docker buildx build --platform linux/amd64 --push` |
| Registry API | net/http against the v2 API (catalog, tags) |
| YAML/DSL | `goccy/go-yaml` (better errors, line numbers for validation msgs) |
| Config schema | Go structs + hand-rolled validation → publish JSON Schema for editor autocomplete |

### 6.2 Repo layout

```
jekyo/
  cmd/jekyo/            # cobra commands (thin)
  internal/
    sshx/               # ssh exec, file push, streaming output
    provision/          # installer steps + preflight + uninstall (each step: Check/Apply)
    addons/             # embedded manifests: contour/ kcert/ registry/ vpn/ nvidia/
    dsl/                # schema, parse, validate, interpolate
    compile/            # dsl -> k8s objects
    deploy/             # apply, prune, release records, rollback
    build/              # docker buildx orchestration, content hashing
    kube/               # client-go helpers, port-forward, logs, exec
    registry/           # v2 API client
    dashboard/          # http handlers, html/template views, htmx assets, sse
    context/            # ~/.jekyo state
  docs/
  examples/             # web+db, gpu-llm, static-site
```

Installer steps implement `Check() / Apply()` so `install` is convergent and
`server info` can report per-addon health from the same code.

### 6.3 Milestones

- **M0 — skeleton (small):** repo, cobra, context store, `jekyo kubectl`.
- **M1 — installer:** preflight, k3s, addons, GPU, kubeconfig, uninstall.
  *Done when:* fresh Ubuntu box → `server install` → green `server info`;
  `uninstall` returns it to clean.
- **M2 — DSL core:** parse/validate/render + `up/down/ls/render` with
  `image:`-only services. *Done when:* the old vllm/typesense YAMLs are
  ~15-line jekyo.yamls that deploy with ingress+TLS+PVC.
- **M3 — builds:** `build:` support, buildx, registry push/auth, content-
  hash skip, `images`. *Done when:* `up` on a Dockerfile app goes source →
  URL in one command.
- **M4 — day-2 UX:** logs/exec/ps/status/restart/rollback/history,
  `schedule:` → CronJob, `jekyo init` + GitHub-hosted template catalog,
  `skill install` + `schema`, CI deploy recipe, polish, docs, examples.
- **M5 — dashboard:** local mode with all six views + SSE log tail;
  in-cluster deploy (read-only SA, `--vpn-only` / basic-auth ingress);
  `?embed=1`. **← v1 release**
- **M6 — backups + in-cluster builds (v1.1):**
  - DONE: `backup:` per volume (cron schedule + S3 target) compiling to
    restic CronJobs; `jekyo backup config|now|ls|restore`; S3 credentials
    stored once per cluster; `jekyo init` offers schedules per volume.
  - **in-cluster BuildKit**: buildkitd addon + pure-Go moby/buildkit
    client over the SSH transport — context streamed up, image pushed to
    the internal registry, builds native on the server's arch. Removes
    the local-docker requirement entirely (docker stays as fallback);
    end state: the only thing a user installs is the jekyo binary.
    (kubectl is already optional — everything uses client-go; only the
    `jekyo kubectl` escape hatch shells out.)
- **v2 themes:** tenants (quotas, network policies, per-tenant registry
  scope), `server join` (multi-node), crashloop webhook notifications
  (opt-in), PR preview environments.

