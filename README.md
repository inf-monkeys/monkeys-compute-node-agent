# monkeys-compute-node-agent

`monkeys-compute-node-agent` runs on VPS or bare-metal servers and connects back to the Monkeys compute control plane.

The first milestone is intentionally small:

- register with `/api/compute/node-agent/register` using a bootstrap token
- store the returned agent token locally
- discover local OS/CPU/memory/GPU/Kubernetes/HAMi facts
- send heartbeats to `/api/compute/node-agent/heartbeat`
- poll `/api/compute/node-agent/plan`
- report plan events and completion

It does not install K3s or HAMi automatically yet. Those actions are modeled as future plan executors.

## Build

```bash
go build ./cmd/monkeys-compute-node-agent
```

Linux release binaries:

```bash
make build-linux
```

## Install

Online install from a published release:

```bash
curl -fsSL https://compute.example.com/api/compute/node-agent/install.sh | \
  sudo MONKEYS_SERVER=https://compute.example.com MONKEYS_BOOTSTRAP_TOKEN=mnbt_xxx sh -
```

Install with a local binary, useful before the first release pipeline exists:

```bash
sudo MONKEYS_SERVER=https://compute.example.com \
  MONKEYS_BOOTSTRAP_TOKEN=mnbt_xxx \
  MONKEYS_AGENT_BINARY=/path/to/monkeys-compute-node-agent \
  sh scripts/install.sh
```

The installer:

- installs the binary to `/usr/local/bin/monkeys-compute-node-agent`
- writes `/etc/monkeys-compute-node-agent/agent.env`
- registers the node and writes `/var/lib/monkeys-compute-node-agent/agent-state.json`
- creates and starts `monkeys-compute-node-agent.service` when systemd is available

## Register

```bash
monkeys-compute-node-agent register \
  --server https://compute.example.com \
  --bootstrap-token mnbt_xxx
```

## Run

```bash
monkeys-compute-node-agent run \
  --server https://compute.example.com
```

By default the state file is `agent-state.json` in the current directory. For systemd deployments, pass `--state /var/lib/monkeys-compute-node-agent/agent-state.json`.

## Current Plan Actions

The first executable plan actions are intentionally non-destructive:

- `agent.register`
- `inspect`
- `noop`

Unknown actions are reported back as warning events. K3s and HAMi mutation actions should be added behind explicit plan action types and tests.
