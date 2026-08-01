# Deploy from CI (push-to-deploy)

JEKYO has no server-side git integration by design — CI just runs the same
CLI you do. One-time setup:

```sh
jekyo context export my-server   # prints a base64 blob
```

Store the blob as a `JEKYO_CONTEXT` secret in your repo, and (only if your
app has `build:` services) an SSH private key with access to the server as
`JEKYO_SSH_KEY`.

## GitHub Actions

```yaml
name: deploy
on:
  push:
    branches: [main]

jobs:
  deploy:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4

      - name: Install jekyo
        run: |
          curl -fsSL https://github.com/jekyo/jekyo/releases/latest/download/jekyo-linux-amd64 \
            -o /usr/local/bin/jekyo && chmod +x /usr/local/bin/jekyo

      - name: Restore context
        run: echo "${{ secrets.JEKYO_CONTEXT }}" | jekyo context import

      - name: Restore SSH key (only needed for build: services)
        run: |
          mkdir -p ~/.ssh
          echo "${{ secrets.JEKYO_SSH_KEY }}" > ~/.ssh/jekyo
          chmod 600 ~/.ssh/jekyo

      - name: Deploy
        run: jekyo up --ssh-key ~/.ssh/jekyo
```

Notes:

- `jekyo up` is idempotent and content-hashed: unchanged services don't
  rebuild, unchanged manifests are no-ops.
- The kubeconfig in the context points at the server's public IP:6443;
  ensure CI can reach it (or restrict 6443 to CI egress IPs with ufw).
- PR previews: `jekyo up` with a modified `app:` name (e.g. rewrite to
  `myapp-pr-42` in the workflow) gives every PR an isolated namespace;
  `jekyo down myapp-pr-42 --volumes` on PR close cleans it up.
