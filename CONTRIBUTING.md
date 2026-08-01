# Contributing to JEKYO

Thanks for considering a contribution. This document is short on
ceremony and long on the things that actually get PRs merged.

## Development setup

You need Go 1.23+ and make.

```sh
git clone https://github.com/jekyo/jekyo
cd jekyo
make build   # produces ./jekyo
make test    # go test ./...
```

For end-to-end work you need a throwaway Ubuntu server. A
[multipass](https://multipass.run) VM works well:

```sh
multipass launch --name jekyo-test --disk 20G --memory 4G
jekyo server install ubuntu@<vm-ip> --ip <vm-ip> --ssh-key <key>
```

`jekyo server uninstall` returns the VM to clean, so one VM survives
many iterations.

## Where things live

| Path | What it is |
| --- | --- |
| `cmd/jekyo/` | Command definitions (cobra), one file per area |
| `internal/dsl/` | jekyo.yaml types, parsing, validation |
| `internal/compile/` | DSL to Kubernetes objects |
| `internal/deploy/` | Server-side apply, releases, rollback |
| `internal/provision/` | Preflight, k3s install, uninstall |
| `internal/addons/` | Embedded manifests (ingress, registry, TLS, VPN) |
| `internal/sshx/` | SSH client used for everything server-side |
| `internal/templates/` | Template catalog client and input resolution |
| `internal/skillpack/` | The reference AI agents learn from |
| `SPEC.md` | The authoritative specification |

If a change alters behavior described in `SPEC.md`, update the spec in
the same PR. If it changes the DSL or CLI surface, update
`internal/skillpack/reference.md` too; agents read that file.

## Pull requests

- Conventional commit titles: `feat:`, `fix:`, `docs:`, `refactor:`,
  `test:`, `chore:`.
- `make test` must pass. New behavior needs a test that fails without
  the change.
- User-visible changes get a line in `CHANGELOG.md` under a
  `[Unreleased]` heading (create it if absent).
- Keep PRs focused. Two small PRs merge faster than one mixed one.
- `gofmt` formatting, and code comments only where the code cannot
  explain itself.

## Templates

App templates live in
[jekyo/templates](https://github.com/jekyo/templates). Template fixes
and additions go there, not here.

## Reporting bugs and requesting features

Use the issue templates. For anything security-sensitive, do not open
an issue; see [SECURITY.md](SECURITY.md).
