# monkeys-compute-node-agent

`monkeys-compute-node-agent` runs on VPS or bare-metal servers and connects back to the Monkeys compute control plane.

The first milestone is intentionally small:

- register with `/api/compute/node-agent/register` using a bootstrap token
- store the returned agent token locally
- discover local OS/CPU/memory/GPU/Kubernetes/HAMi facts
- send heartbeats to `/api/compute/node-agent/heartbeat`
- poll `/api/compute/node-agent/plan`
- report plan events and completion
- prepare install commands for `k3s.install-server`, `k3s.join-agent`, and `hami.install`

Install actions are dry-run by default. To actually execute install commands, pass `--allow-install` and set `--dry-run=false` on a Linux host.

## Build

```bash
make build
make build-linux
make test
```

`make build-linux` creates:

- `dist/monkeys-compute-node-agent_linux_amd64`
- `dist/monkeys-compute-node-agent_linux_arm64`

Publish those files to the release location used by the control-plane install script.

## Register

```bash
monkeys-compute-node-agent register \
  --server https://compute.example.com \
  --bootstrap-token mnbt_xxx
```

## Run

```bash
monkeys-compute-node-agent run \
  --server https://compute.example.com \
  --allow-install \
  --dry-run=false
```

By default the state file is `agent-state.json` in the current directory. For systemd deployments, pass `--state /var/lib/monkeys-compute-node-agent/agent-state.json`.

## Install Script

The control plane exposes:

```bash
curl -fsSL "$MONKEYS_SERVER/api/compute/node-agent/install.sh" \
  | sudo MONKEYS_SERVER="$MONKEYS_SERVER" MONKEYS_BOOTSTRAP_TOKEN="$MONKEYS_BOOTSTRAP_TOKEN" sh -
```

The script downloads the Linux binary, registers the server, writes `/etc/monkeys-compute-node-agent/agent.env`, installs a systemd service, and starts the agent.

Useful overrides:

- `MONKEYS_AGENT_DOWNLOAD_URL`: exact binary URL.
- `MONKEYS_AGENT_RELEASE_BASE_URL`: release base URL, defaulting to GitHub Releases.
- `MONKEYS_AGENT_VERSION`: release version, defaulting to `latest`.
- `MONKEYS_ALLOW_INSTALL=true` and `MONKEYS_DRY_RUN=false`: allow install plans such as K3s/HAMi to execute.
