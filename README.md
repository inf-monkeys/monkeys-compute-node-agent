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
