#!/bin/sh
set -eu

repo_dir="$(CDPATH= cd -- "$(dirname "$0")/.." && pwd)"
installer="$repo_dir/scripts/install.sh"
tmp_dir="$(mktemp -d)"
trap 'rm -rf "$tmp_dir"' EXIT HUP INT TERM

fail() {
  echo "test failure: $*" >&2
  exit 1
}

fake_agent="$tmp_dir/fake-agent"
fake_bin="$tmp_dir/fake-bin"
mkdir -p "$fake_bin"
cat > "$fake_bin/uname" <<'EOF'
#!/bin/sh
case "${1:-}" in
  -s) echo Linux ;;
  -m) echo x86_64 ;;
  *) echo Linux ;;
esac
EOF
chmod 0755 "$fake_bin/uname"

cat > "$fake_agent" <<'EOF'
#!/bin/sh
set -eu
case "${1:-}" in
  register)
    [ "${MONKEYS_BOOTSTRAP_TOKEN:-}" = "bootstrap-secret" ] || exit 19
    state=""
    while [ "$#" -gt 0 ]; do
      if [ "$1" = "--state" ]; then
        state="$2"
        shift 2
      else
        shift
      fi
    done
    [ -n "$state" ] || exit 20
    umask 077
    printf '{"agentToken":"test-token"}\n' > "$state"
    ;;
  run) exit 0 ;;
  *) exit 21 ;;
esac
EOF
chmod 0755 "$fake_agent"

home_dir="$tmp_dir/home"
mkdir -p "$home_dir"
HOME="$home_dir" \
PATH="$fake_bin:$PATH" \
MONKEYS_SERVER="https://compute.example.test" \
MONKEYS_BOOTSTRAP_TOKEN="bootstrap-secret" \
MONKEYS_AGENT_MODE="worker" \
MONKEYS_AGENT_BINARY="$fake_agent" \
MONKEYS_AGENT_RUN_MODE="none" \
MONKEYS_AGENT_INSTALL_DIR="$tmp_dir/bin" \
MONKEYS_AGENT_CONFIG_DIR="$tmp_dir/config" \
MONKEYS_AGENT_STATE_DIR="$tmp_dir/state" \
sh "$installer" > "$tmp_dir/output"

[ -x "$tmp_dir/bin/monkeys-compute-node-agent" ] || fail "binary was not installed"
[ -f "$tmp_dir/state/agent-state.json" ] || fail "registration state was not created"
if stat -c '%a' "$tmp_dir/config/agent.env" >/dev/null 2>&1; then
  env_mode="$(stat -c '%a' "$tmp_dir/config/agent.env")"
else
  env_mode="$(stat -f '%Lp' "$tmp_dir/config/agent.env")"
fi
[ "$env_mode" = "600" ] || fail "environment file is not mode 0600"
if grep -R "bootstrap-secret" "$tmp_dir/config" "$tmp_dir/state" "$tmp_dir/output" >/dev/null 2>&1; then
  fail "bootstrap token leaked into persistent files or output"
fi
grep -q 'MONKEYS_AGENT_MODE="worker"' "$tmp_dir/config/agent.env" || fail "worker mode was not persisted"

distribution_dir="$tmp_dir/distribution"
distribution_install_dir="$tmp_dir/distribution-bin"
distribution_config_dir="$tmp_dir/distribution-config"
distribution_state_dir="$tmp_dir/distribution-state"
mkdir -p "$distribution_dir"
cp "$fake_agent" "$distribution_dir/monkeys-compute-node-agent_linux_amd64"
if command -v sha256sum >/dev/null 2>&1; then
  distribution_checksum="$(sha256sum "$distribution_dir/monkeys-compute-node-agent_linux_amd64" | awk '{print $1}')"
else
  distribution_checksum="$(shasum -a 256 "$distribution_dir/monkeys-compute-node-agent_linux_amd64" | awk '{print $1}')"
fi
printf '%s  %s\n' "$distribution_checksum" monkeys-compute-node-agent_linux_amd64 > "$distribution_dir/SHA256SUMS"
HOME="$home_dir" \
PATH="$fake_bin:$PATH" \
MONKEYS_SERVER="https://compute.example.test" \
MONKEYS_BOOTSTRAP_TOKEN="bootstrap-secret" \
MONKEYS_AGENT_MODE="worker" \
MONKEYS_AGENT_DISTRIBUTION_BASE_URL="file://$distribution_dir" \
MONKEYS_AGENT_RUN_MODE="none" \
MONKEYS_AGENT_INSTALL_DIR="$distribution_install_dir" \
MONKEYS_AGENT_CONFIG_DIR="$distribution_config_dir" \
MONKEYS_AGENT_STATE_DIR="$distribution_state_dir" \
sh "$installer" > "$tmp_dir/distribution-output"
[ -x "$distribution_install_dir/monkeys-compute-node-agent" ] || fail "Server distribution binary was not installed"
[ -f "$distribution_state_dir/agent-state.json" ] || fail "Server distribution installation did not register"

bad_output="$tmp_dir/bad-output"
if HOME="$home_dir" \
  PATH="$fake_bin:$PATH" \
  MONKEYS_SERVER="https://compute.example.test" \
  MONKEYS_BOOTSTRAP_TOKEN="bootstrap-secret" \
  MONKEYS_AGENT_MODE="invalid" \
  MONKEYS_AGENT_BINARY="$fake_agent" \
  MONKEYS_AGENT_RUN_MODE="none" \
  sh "$installer" > "$bad_output" 2>&1; then
  fail "invalid mode was accepted"
fi
grep -q "must be host, cluster, or worker" "$bad_output" || fail "invalid-mode error was unclear"

echo "install script tests passed"
