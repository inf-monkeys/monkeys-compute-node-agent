#!/bin/sh
set -eu

SERVICE_NAME="monkeys-compute-node-agent"
MODE="${MONKEYS_AGENT_MODE:-host}"
RUN_MODE="${MONKEYS_AGENT_RUN_MODE:-auto}"
VERSION="${MONKEYS_AGENT_VERSION:-latest}"
RELEASE_BASE_URL="${MONKEYS_AGENT_RELEASE_BASE_URL:-https://github.com/inf-monkeys/monkeys-compute-node-agent/releases}"
DISTRIBUTION_BASE_URL="${MONKEYS_AGENT_DISTRIBUTION_BASE_URL:-}"
INTERVAL="${MONKEYS_AGENT_INTERVAL:-30s}"
ALLOW_INSTALL="${MONKEYS_ALLOW_INSTALL:-false}"
DRY_RUN="${MONKEYS_DRY_RUN:-true}"
TARGET_ID="${MONKEYS_TARGET_ID:-}"

fail() {
  echo "error: $*" >&2
  exit 1
}

need_cmd() {
  command -v "$1" >/dev/null 2>&1 || fail "$1 is required"
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

sha256_file() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
  elif command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$1" | awk '{print $1}'
  else
    fail "sha256sum or shasum is required"
  fi
}

install_file() {
  source_path="$1"
  target_path="$2"
  mode="$3"
  mkdir -p "$(dirname "$target_path")"
  cp "$source_path" "$target_path"
  chmod "$mode" "$target_path"
}

escape_env_file_value() {
  printf '%s' "$1" | sed 's/\\/\\\\/g; s/"/\\"/g'
}

write_env_file() {
  env_path="$1"
  umask 077
  {
    printf 'MONKEYS_SERVER="%s"\n' "$(escape_env_file_value "$MONKEYS_SERVER")"
    printf 'MONKEYS_AGENT_MODE="%s"\n' "$(escape_env_file_value "$MODE")"
    printf 'MONKEYS_AGENT_STATE="%s"\n' "$(escape_env_file_value "$STATE_FILE")"
    printf 'MONKEYS_AGENT_WORKSPACE="%s"\n' "$(escape_env_file_value "$WORKSPACE_DIR")"
    printf 'MONKEYS_AGENT_INTERVAL="%s"\n' "$(escape_env_file_value "$INTERVAL")"
    printf 'MONKEYS_ALLOW_INSTALL="%s"\n' "$(escape_env_file_value "$ALLOW_INSTALL")"
    printf 'MONKEYS_DRY_RUN="%s"\n' "$(escape_env_file_value "$DRY_RUN")"
    if [ -n "$TARGET_ID" ]; then
      printf 'MONKEYS_TARGET_ID="%s"\n' "$(escape_env_file_value "$TARGET_ID")"
    fi
  } > "$env_path"
  chmod 0600 "$env_path"
}

run_in_background() {
  log_file="$STATE_DIR/agent.log"
  pid_file="$STATE_DIR/agent.pid"
  if [ -f "$pid_file" ]; then
    old_pid="$(cat "$pid_file" 2>/dev/null || true)"
    if [ -n "$old_pid" ] && kill -0 "$old_pid" 2>/dev/null; then
      fail "$SERVICE_NAME is already running as pid $old_pid"
    fi
  fi
  # The token is held only in the 0600 state file; it is never placed in argv.
  umask 077
  nohup "$INSTALL_DIR/$SERVICE_NAME" run \
    --mode "$MODE" \
    --server "$MONKEYS_SERVER" \
    --state "$STATE_FILE" \
    --workspace "$WORKSPACE_DIR" \
    --interval "$INTERVAL" \
    --allow-install="$ALLOW_INSTALL" \
    --dry-run="$DRY_RUN" \
    >"$log_file" 2>&1 </dev/null &
  agent_pid=$!
  printf '%s\n' "$agent_pid" > "$pid_file"
  chmod 0600 "$pid_file"
  sleep 1
  kill -0 "$agent_pid" 2>/dev/null || fail "$SERVICE_NAME stopped during startup; inspect $log_file"
  echo "$SERVICE_NAME started as pid $agent_pid. Logs: $log_file"
}

install_system_service() {
  service_file="$tmp_dir/$SERVICE_NAME.service"
  cat > "$service_file" <<EOF
[Unit]
Description=Monkeys Compute Agent (${MODE})
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
EnvironmentFile=${CONFIG_DIR}/agent.env
ExecStart=${INSTALL_DIR}/${SERVICE_NAME} run --mode ${MODE} --server \${MONKEYS_SERVER} --state ${STATE_FILE} --workspace ${WORKSPACE_DIR} --interval \${MONKEYS_AGENT_INTERVAL} --allow-install=\${MONKEYS_ALLOW_INSTALL} --dry-run=\${MONKEYS_DRY_RUN}
Restart=always
RestartSec=5
User=root
WorkingDirectory=${STATE_DIR}
NoNewPrivileges=false

[Install]
WantedBy=multi-user.target
EOF
  install_file "$service_file" "/etc/systemd/system/$SERVICE_NAME.service" 0644
  systemctl daemon-reload
  systemctl enable --now "$SERVICE_NAME"
  echo "$SERVICE_NAME installed and started with systemd."
}

install_user_service() {
  service_dir="${XDG_CONFIG_HOME:-$HOME/.config}/systemd/user"
  service_file="$tmp_dir/$SERVICE_NAME.service"
  cat > "$service_file" <<EOF
[Unit]
Description=Monkeys Compute Agent (${MODE})
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
EnvironmentFile=${CONFIG_DIR}/agent.env
ExecStart=${INSTALL_DIR}/${SERVICE_NAME} run --mode ${MODE} --server \${MONKEYS_SERVER} --state ${STATE_FILE} --workspace ${WORKSPACE_DIR} --interval \${MONKEYS_AGENT_INTERVAL} --allow-install=\${MONKEYS_ALLOW_INSTALL} --dry-run=\${MONKEYS_DRY_RUN}
Restart=always
RestartSec=5
WorkingDirectory=${STATE_DIR}

[Install]
WantedBy=default.target
EOF
  install_file "$service_file" "$service_dir/$SERVICE_NAME.service" 0644
  systemctl --user daemon-reload
  systemctl --user enable --now "$SERVICE_NAME"
  echo "$SERVICE_NAME installed and started with user systemd."
}

[ "$(uname -s)" = "Linux" ] || fail "Linux is required"
[ -n "${MONKEYS_SERVER:-}" ] || fail "MONKEYS_SERVER is required"
[ -n "${MONKEYS_BOOTSTRAP_TOKEN:-}" ] || fail "MONKEYS_BOOTSTRAP_TOKEN is required for installation"
case "$MONKEYS_SERVER" in
  http://*|https://*) ;;
  *) fail "MONKEYS_SERVER must be an http:// or https:// URL" ;;
esac
case "$MONKEYS_SERVER$TARGET_ID" in
  *[[:space:]]*) fail "server and target values must not contain whitespace" ;;
  *\"*|*\'*) fail "server and target values must not contain quote characters" ;;
esac

case "$MODE" in
  host|cluster|worker) ;;
  *) fail "MONKEYS_AGENT_MODE must be host, cluster, or worker" ;;
esac
case "$RUN_MODE" in
  auto|systemd|user-systemd|background|foreground|none) ;;
  *) fail "MONKEYS_AGENT_RUN_MODE must be auto, systemd, user-systemd, background, foreground, or none" ;;
esac
case "$ALLOW_INSTALL:$DRY_RUN" in
  true:true|true:false|false:true|false:false) ;;
  *) fail "MONKEYS_ALLOW_INSTALL and MONKEYS_DRY_RUN must be true or false" ;;
esac
case "$INTERVAL" in
  ''|*[!0-9smh.]*|.*) fail "MONKEYS_AGENT_INTERVAL must be a positive Go duration such as 30s or 1m" ;;
esac

need_cmd uname
need_cmd id
need_cmd mktemp

is_root=false
if [ "$(id -u)" -eq 0 ]; then
  is_root=true
fi
[ "$MODE" != "host" ] || [ "$is_root" = true ] || fail "host mode requires root; run the installer through sudo or choose worker mode"

if [ "$is_root" = true ]; then
  INSTALL_DIR="${MONKEYS_AGENT_INSTALL_DIR:-/usr/local/bin}"
  CONFIG_DIR="${MONKEYS_AGENT_CONFIG_DIR:-/etc/monkeys-compute-node-agent}"
  STATE_DIR="${MONKEYS_AGENT_STATE_DIR:-/var/lib/monkeys-compute-node-agent}"
else
  INSTALL_DIR="${MONKEYS_AGENT_INSTALL_DIR:-$HOME/.local/bin}"
  CONFIG_DIR="${MONKEYS_AGENT_CONFIG_DIR:-${XDG_CONFIG_HOME:-$HOME/.config}/monkeys-compute-node-agent}"
  STATE_DIR="${MONKEYS_AGENT_STATE_DIR:-${XDG_STATE_HOME:-$HOME/.local/state}/monkeys-compute-node-agent}"
fi
STATE_FILE="${MONKEYS_AGENT_STATE:-$STATE_DIR/agent-state.json}"
WORKSPACE_DIR="${MONKEYS_AGENT_WORKSPACE:-$STATE_DIR/workspace}"
case "$INSTALL_DIR$CONFIG_DIR$STATE_DIR$STATE_FILE$WORKSPACE_DIR" in
  *[[:space:]]*) fail "install, config, state, and workspace paths must not contain whitespace" ;;
  *\"*|*\'*) fail "install, config, state, and workspace paths must not contain quote characters" ;;
esac

tmp_dir="$(mktemp -d)"
trap 'rm -rf "$tmp_dir"' EXIT HUP INT TERM

agent_arch="$(detect_arch)"
asset_name="${SERVICE_NAME}_linux_${agent_arch}"
binary_path="$tmp_dir/$asset_name"
if [ -n "${MONKEYS_AGENT_BINARY:-}" ]; then
  [ -f "$MONKEYS_AGENT_BINARY" ] || fail "MONKEYS_AGENT_BINARY does not exist: $MONKEYS_AGENT_BINARY"
  cp "$MONKEYS_AGENT_BINARY" "$binary_path"
else
  if [ -n "${MONKEYS_AGENT_DOWNLOAD_URL:-}" ]; then
    binary_url="$MONKEYS_AGENT_DOWNLOAD_URL"
  elif [ -n "$DISTRIBUTION_BASE_URL" ]; then
    binary_url="${DISTRIBUTION_BASE_URL%/}/${asset_name}"
  elif [ "$VERSION" = "latest" ]; then
    binary_url="${RELEASE_BASE_URL}/latest/download/${asset_name}"
  else
    binary_url="${RELEASE_BASE_URL}/download/${VERSION}/${asset_name}"
  fi
  download "$binary_url" "$binary_path"
fi

expected_checksum="${MONKEYS_AGENT_SHA256:-}"
if [ -z "$expected_checksum" ] && [ -z "${MONKEYS_AGENT_BINARY:-}" ]; then
  checksum_file="$tmp_dir/SHA256SUMS"
  if [ -n "${MONKEYS_AGENT_CHECKSUMS_URL:-}" ]; then
    checksums_url="$MONKEYS_AGENT_CHECKSUMS_URL"
  elif [ -n "$DISTRIBUTION_BASE_URL" ]; then
    checksums_url="${DISTRIBUTION_BASE_URL%/}/SHA256SUMS"
  elif [ "$VERSION" = "latest" ]; then
    checksums_url="${RELEASE_BASE_URL}/latest/download/SHA256SUMS"
  else
    checksums_url="${RELEASE_BASE_URL}/download/${VERSION}/SHA256SUMS"
  fi
  download "$checksums_url" "$checksum_file"
  expected_checksum="$(awk -v asset="$asset_name" '$2 == asset || $2 == "*" asset { print $1; exit }' "$checksum_file")"
  [ -n "$expected_checksum" ] || fail "checksum for $asset_name was not found in SHA256SUMS"
fi
if [ -n "$expected_checksum" ]; then
  actual_checksum="$(sha256_file "$binary_path")"
  [ "$actual_checksum" = "$expected_checksum" ] || fail "checksum verification failed for $asset_name"
fi
chmod 0755 "$binary_path"

mkdir -p "$INSTALL_DIR" "$CONFIG_DIR" "$STATE_DIR" "$WORKSPACE_DIR"
chmod 0700 "$CONFIG_DIR" "$STATE_DIR"
chmod 0700 "$WORKSPACE_DIR"
install_file "$binary_path" "$INSTALL_DIR/$SERVICE_NAME" 0755
write_env_file "$CONFIG_DIR/agent.env"

echo "registering $MODE target with the compute control plane..."
"$INSTALL_DIR/$SERVICE_NAME" register \
  --mode "$MODE" \
  --server "$MONKEYS_SERVER" \
  --target-id "$TARGET_ID" \
  --state "$STATE_FILE" \
  --workspace "$WORKSPACE_DIR"
unset MONKEYS_BOOTSTRAP_TOKEN

if [ "$RUN_MODE" = "auto" ]; then
  if [ "$is_root" = true ] && command -v systemctl >/dev/null 2>&1 && [ -d /run/systemd/system ]; then
    RUN_MODE=systemd
  elif [ "$is_root" = false ] && command -v systemctl >/dev/null 2>&1 && systemctl --user show-environment >/dev/null 2>&1; then
    RUN_MODE=user-systemd
  else
    RUN_MODE=background
  fi
fi

case "$RUN_MODE" in
  systemd)
    [ "$is_root" = true ] || fail "systemd installation requires root; use user-systemd or background"
    command -v systemctl >/dev/null 2>&1 || fail "systemctl is required"
    install_system_service
    ;;
  user-systemd)
    [ "$is_root" = false ] || fail "user-systemd is intended for a non-root user"
    command -v systemctl >/dev/null 2>&1 || fail "systemctl is required"
    install_user_service
    ;;
  background)
    run_in_background
    ;;
  foreground)
    exec "$INSTALL_DIR/$SERVICE_NAME" run \
      --mode "$MODE" \
      --server "$MONKEYS_SERVER" \
      --state "$STATE_FILE" \
      --workspace "$WORKSPACE_DIR" \
      --interval "$INTERVAL" \
      --allow-install="$ALLOW_INSTALL" \
      --dry-run="$DRY_RUN"
    ;;
  none)
    echo "$SERVICE_NAME installed and registered. Start it with:"
    echo "  $INSTALL_DIR/$SERVICE_NAME run --mode $MODE --server '$MONKEYS_SERVER' --state '$STATE_FILE' --workspace '$WORKSPACE_DIR'"
    ;;
esac
