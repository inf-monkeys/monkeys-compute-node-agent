# Monkeys Compute Agent

`monkeys-compute-node-agent` is the single Linux Agent used by Monkeys Compute. One binary runs in three deliberately different trust and resource boundaries:

| Mode | Runs on | Control boundary |
| --- | --- | --- |
| `host` | A dedicated Linux or bare-metal server | Host inventory, system services, storage/network/GPU/CPU observation, K3s/HAMi plans, and Kubernetes runtime operations. |
| `cluster` | A privileged Agent pod in an existing Kubernetes cluster | Cluster-wide inventory and Kubernetes operations permitted by the installed ServiceAccount. It does not manage the underlying hosts. |
| `worker` | A rented container or pod that behaves like a Linux workspace | Trusted workload process lifecycle, workspace files, localhost HTTP, Git, and resource observation. Upstream CPU/GPU/storage allocation remains immutable. |

The control plane may install the Agent over SSH, or an operator can run the same install script directly. Both paths produce the same binary, state format, and lifecycle.

## Security Model

- A bootstrap token is short lived and is used only by `register`. The Agent exchanges it for an Agent token stored in a `0600` state file.
- The installer never writes the bootstrap token to configuration, service arguments, logs, or the state directory.
- Direct-token Cluster pods should receive `MONKEYS_AGENT_TOKEN` from a Kubernetes Secret. Passing `--token` is supported for debugging but exposes it to the process list.
- Release binaries are verified against `SHA256SUMS`. A control plane can pin an exact checksum with `MONKEYS_AGENT_SHA256`.
- `host` mode is intentionally privileged. Install execution remains opt-in through `MONKEYS_ALLOW_INSTALL=true` and `MONKEYS_DRY_RUN=false`.
- `cluster` authority is exactly the ServiceAccount RBAC granted during onboarding. Cluster-wide management therefore requires cluster-wide RBAC and should be installed only by a cluster administrator.
- `worker` mode is for trusted workloads inside a resource boundary owned by the rental provider. Multiple workloads under the same Unix UID are not a security isolation boundary.

## Build And Test

Go 1.24 or newer is required.

```bash
make test
make build
make verify-dist
```

`make build-linux` generates the release `dist/` directory:

```text
dist/install.sh
dist/monkeys-compute-node-agent_linux_amd64
dist/monkeys-compute-node-agent_linux_arm64
dist/SHA256SUMS
```

The Linux binaries are statically linked (`CGO_ENABLED=0`). `VERSION=v1.2.3 make build-linux` embeds a release version; otherwise the build tool uses `git describe`.

## Cluster Agent Image

Existing Kubernetes clusters run the same Agent from:

```text
ghcr.io/inf-monkeys/monkeys-compute-node-agent:<tag>
```

The image is multi-architecture (`linux/amd64` and `linux/arm64`), runs as UID/GID `65532`, includes CA certificates and a checksum-pinned `kubectl`, and defaults to `run --mode cluster`. The control plane must inject `MONKEYS_SERVER`, `MONKEYS_TARGET_ID`, and `MONKEYS_AGENT_TOKEN`; the token belongs in a Kubernetes Secret and is never part of the image or pod arguments. `/tmp` is the only writable path required, so the pod can use a read-only root filesystem with an `emptyDir` mounted at `/tmp`.

Build and exercise the current-platform image locally:

```bash
make container-build
make container-test
```

The image health check verifies that the Agent binary remains executable. Compute heartbeats are the authoritative connectivity/readiness signal; a local process health check cannot prove that the control plane or Kubernetes API is reachable.

## Register Then Run

Host and Worker installations normally use a bootstrap token:

```bash
monkeys-compute-node-agent register \
  --mode worker \
  --server https://compute.example.com \
  --bootstrap-token mnbt_xxx \
  --target-id optional-precreated-target-id \
  --state "$HOME/.local/state/monkeys-compute-node-agent/agent-state.json" \
  --workspace "$HOME/.local/state/monkeys-compute-node-agent/workspace"

monkeys-compute-node-agent run \
  --mode worker \
  --server https://compute.example.com \
  --state "$HOME/.local/state/monkeys-compute-node-agent/agent-state.json"
```

A Cluster pod can run without persistent state when the control plane supplies its target and Agent token:

```bash
MONKEYS_AGENT_TOKEN=mnat_xxx \
monkeys-compute-node-agent run \
  --mode cluster \
  --server https://compute.example.com \
  --target-id cluster-target-id \
  --state /tmp/agent-state.json \
  --workspace /tmp/monkeys-compute-agent
```

`once` accepts the same connection flags and performs one heartbeat/task cycle.

## Install Script

The canonical script is `scripts/install.sh`; releases also include it as `dist/install.sh`. Compute Server exposes a small tenant-local bootstrap script at `/compute-agent/install.sh`. That bootstrap validates its inputs, downloads this canonical installer and `SHA256SUMS` from the Agent release repository, verifies the installer, and then runs it. Server does not store or proxy Agent binaries.

### Dedicated Host

Host mode deliberately requires root because a non-root process cannot honestly provide host control. Run as root to install `/usr/local/bin/monkeys-compute-node-agent`, register the host, and enable a system service:

```bash
curl -fsSL "$MONKEYS_SERVER/compute-agent/install.sh" | \
  MONKEYS_SERVER="$MONKEYS_SERVER" \
  MONKEYS_BOOTSTRAP_TOKEN="$MONKEYS_BOOTSTRAP_TOKEN" \
  MONKEYS_AGENT_MODE=host \
  MONKEYS_ALLOW_INSTALL=true \
  MONKEYS_DRY_RUN=false \
  sh -
```

Use `sudo -E sh -` only when your sudo policy explicitly preserves the required variables. Prefer passing environment variables after `sudo` rather than exporting secrets globally.

### Non-root Worker Or Rented Pod

Without root, the installer uses `$HOME/.local/bin`, XDG config/state paths, and user systemd when available. Minimal containers fall back to a verified `nohup` process:

```bash
curl -fsSL "$MONKEYS_SERVER/compute-agent/install.sh" | \
  MONKEYS_SERVER="$MONKEYS_SERVER" \
  MONKEYS_BOOTSTRAP_TOKEN="$MONKEYS_BOOTSTRAP_TOKEN" \
  MONKEYS_AGENT_MODE=worker \
  sh -
```

For a container entrypoint, keep PID 1 supervision explicit:

```bash
MONKEYS_AGENT_RUN_MODE=foreground sh install.sh
```

Supported `MONKEYS_AGENT_RUN_MODE` values are `auto`, `systemd`, `user-systemd`, `background`, `foreground`, and `none`. `none` installs and registers without starting the Agent.

Useful distribution overrides:

- `MONKEYS_AGENT_INSTALLER_URL`: exact canonical installer URL used by the tenant-local bootstrap script.
- `MONKEYS_AGENT_INSTALLER_CHECKSUMS_URL`: exact checksum manifest used to verify that downloaded installer.
- `MONKEYS_AGENT_DOWNLOAD_URL`: exact binary URL.
- `MONKEYS_AGENT_CHECKSUMS_URL`: exact `SHA256SUMS` URL.
- `MONKEYS_AGENT_SHA256`: pinned binary SHA-256.
- `MONKEYS_AGENT_RELEASE_BASE_URL`: GitHub Releases-compatible base URL.
- `MONKEYS_AGENT_VERSION`: `latest` or an exact release tag.
- `MONKEYS_AGENT_BINARY`: local binary for offline installation; pair it with `MONKEYS_AGENT_SHA256` when provenance is not otherwise guaranteed.

## Release

Pushing a `v*` tag runs tests, creates static linux/amd64 and linux/arm64 binaries, verifies `SHA256SUMS`, and publishes all four `dist/` artifacts:

```bash
git tag v1.0.0
git push origin v1.0.0
```

Asset names are a contract with the tenant bootstrap, canonical installer, and Agent upgrade flow; do not rename them without updating those consumers.

The same stable `v*` tag also publishes the Cluster Agent image. For `v1.2.3`, GHCR receives `v1.2.3`, `1.2.3`, `1.2`, `1`, and `latest`. Prerelease tags such as `v1.3.0-rc.1` receive only immutable prerelease/version tags and never move `latest` or stable major/minor tags. Binary release assets and container publishing use separate jobs and remain independently consumable.
