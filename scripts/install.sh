#!/bin/sh
set -eu

SERVICE_NAME="monkeys-compute-node-agent"
INSTALL_DIR="${MONKEYS_AGENT_INSTALL_DIR:-/usr/local/bin}"
CONFIG_DIR="${MONKEYS_AGENT_CONFIG_DIR:-/etc/monkeys-compute-node-agent}"
STATE_DIR="${MONKEYS_AGENT_STATE_DIR:-/var/lib/monkeys-compute-node-agent}"
STATE_FILE="${STATE_DIR}/agent-state.json"
INTERVAL="${MONKEYS_AGENT_INTERVAL:-30s}"
VERSION="${MONKEYS_AGENT_VERSION:-latest}"

fail() {
  echo "error: $*" >&2
  exit 1
}

need_cmd() {
  command -v "$1" >/dev/null 2>&1 || fail "$1 is required"
}

as_root() {
  if [ "$(id -u)" -eq 0 ]; then
    "$@"
  else
    need_cmd sudo
    sudo "$@"
  fi
}

detect_arch() {
  arch="$(uname -m)"
  case "$arch" in
    x86_64|amd64) echo "amd64" ;;
    aarch64|arm64) echo "arm64" ;;
    *) fail "unsupported architecture: $arch" ;;
  esac
}

download() {
  url="$1"
  target="$2"
  if command -v curl >/dev/null 2>&1; then
    curl -fsSL "$url" -o "$target"
  elif command -v wget >/dev/null 2>&1; then
    wget -q "$url" -O "$target"
  else
    fail "curl or wget is required"
  fi
}

[ "$(uname -s)" = "Linux" ] || fail "Linux is required"
[ -n "${MONKEYS_SERVER:-}" ] || fail "MONKEYS_SERVER is required"
[ -n "${MONKEYS_BOOTSTRAP_TOKEN:-}" ] || fail "MONKEYS_BOOTSTRAP_TOKEN is required"

need_cmd uname
need_cmd id

tmp_dir="$(mktemp -d)"
trap 'rm -rf "$tmp_dir"' EXIT

binary_path="$tmp_dir/${SERVICE_NAME}"
if [ -n "${MONKEYS_AGENT_BINARY:-}" ]; then
  [ -f "$MONKEYS_AGENT_BINARY" ] || fail "MONKEYS_AGENT_BINARY does not exist: $MONKEYS_AGENT_BINARY"
  cp "$MONKEYS_AGENT_BINARY" "$binary_path"
elif [ -n "${MONKEYS_AGENT_DOWNLOAD_URL:-}" ]; then
  download "$MONKEYS_AGENT_DOWNLOAD_URL" "$binary_path"
else
  agent_arch="$(detect_arch)"
  if [ "$VERSION" = "latest" ]; then
    url="https://github.com/inf-monkeys/monkeys-compute-node-agent/releases/latest/download/${SERVICE_NAME}_linux_${agent_arch}"
  else
    url="https://github.com/inf-monkeys/monkeys-compute-node-agent/releases/download/${VERSION}/${SERVICE_NAME}_linux_${agent_arch}"
  fi
  download "$url" "$binary_path"
fi

chmod +x "$binary_path"

as_root mkdir -p "$INSTALL_DIR" "$CONFIG_DIR" "$STATE_DIR"
as_root install -m 0755 "$binary_path" "$INSTALL_DIR/$SERVICE_NAME"

env_file="$tmp_dir/agent.env"
cat > "$env_file" <<EOF
MONKEYS_SERVER=${MONKEYS_SERVER}
MONKEYS_AGENT_INTERVAL=${INTERVAL}
EOF
as_root install -m 0600 "$env_file" "$CONFIG_DIR/agent.env"

echo "registering node with compute control plane..."
as_root "$INSTALL_DIR/$SERVICE_NAME" register \
  --server "$MONKEYS_SERVER" \
  --bootstrap-token "$MONKEYS_BOOTSTRAP_TOKEN" \
  --state "$STATE_FILE"

if command -v systemctl >/dev/null 2>&1; then
  service_file="$tmp_dir/${SERVICE_NAME}.service"
  cat > "$service_file" <<EOF
[Unit]
Description=Monkeys Compute Node Agent
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
EnvironmentFile=-${CONFIG_DIR}/agent.env
ExecStart=${INSTALL_DIR}/${SERVICE_NAME} run --server \${MONKEYS_SERVER} --state ${STATE_FILE} --interval \${MONKEYS_AGENT_INTERVAL}
Restart=always
RestartSec=5
User=root
WorkingDirectory=${STATE_DIR}
StateDirectory=monkeys-compute-node-agent
RuntimeDirectory=monkeys-compute-node-agent

[Install]
WantedBy=multi-user.target
EOF
  as_root install -m 0644 "$service_file" "/etc/systemd/system/${SERVICE_NAME}.service"
  as_root systemctl daemon-reload
  as_root systemctl enable --now "$SERVICE_NAME"
  echo "${SERVICE_NAME} installed and started."
else
  echo "systemctl not found. Run manually:"
  echo "  $INSTALL_DIR/$SERVICE_NAME run --server $MONKEYS_SERVER --state $STATE_FILE --interval $INTERVAL"
fi
