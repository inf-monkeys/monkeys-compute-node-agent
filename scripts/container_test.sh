#!/bin/sh
set -eu

IMAGE="${MONKEYS_AGENT_CONTAINER_IMAGE:-monkeys-compute-node-agent:container-test}"
EXPECTED_VERSION="${MONKEYS_AGENT_CONTAINER_VERSION:-container-test}"
CONTAINER_ID=""
EXPORT_ID=""

cleanup() {
  if [ -n "$CONTAINER_ID" ]; then
    docker rm -f "$CONTAINER_ID" >/dev/null 2>&1 || true
  fi
  if [ -n "$EXPORT_ID" ]; then
    docker rm -f "$EXPORT_ID" >/dev/null 2>&1 || true
  fi
}
trap cleanup EXIT HUP INT TERM

docker build --build-arg "VERSION=$EXPECTED_VERSION" --load -t "$IMAGE" .

test "$(docker image inspect -f '{{.Config.User}}' "$IMAGE")" = "65532:65532"
test "$(docker run --rm --read-only "$IMAGE" version)" = "$EXPECTED_VERSION"
docker run --rm --read-only --entrypoint /usr/local/bin/kubectl "$IMAGE" \
  version --client=true -o json >/dev/null

image_config="$(docker image inspect -f '{{json .Config.Env}} {{json .Config.Entrypoint}} {{json .Config.Cmd}}' "$IMAGE")"
if printf '%s' "$image_config" | grep -q 'MONKEYS_AGENT_TOKEN'; then
  echo "Agent token must not be stored in image metadata" >&2
  exit 1
fi

EXPORT_ID="$(docker create "$IMAGE" version)"
docker export "$EXPORT_ID" | tar -tf - | grep -q '^etc/ssl/certs/ca-certificates.crt$'
docker rm "$EXPORT_ID" >/dev/null
EXPORT_ID=""

CONTAINER_ID="$(docker run -d --read-only --tmpfs /tmp:rw,nosuid,size=64m --network none \
  -e MONKEYS_SERVER=http://127.0.0.1:1 \
  -e MONKEYS_AGENT_TOKEN=container-smoke-token \
  -e MONKEYS_TARGET_ID=container-smoke-target \
  "$IMAGE")"

attempt=0
while [ "$attempt" -lt 15 ]; do
  health="$(docker inspect -f '{{if .State.Health}}{{.State.Health.Status}}{{else}}missing{{end}}' "$CONTAINER_ID")"
  [ "$health" = "healthy" ] && break
  [ "$health" = "unhealthy" ] && {
    docker logs "$CONTAINER_ID" >&2
    exit 1
  }
  attempt=$((attempt + 1))
  sleep 1
done

test "$(docker inspect -f '{{.State.Running}}' "$CONTAINER_ID")" = "true"
test "$(docker inspect -f '{{.State.Health.Status}}' "$CONTAINER_ID")" = "healthy"
test "$(docker inspect -f '{{.Config.User}}' "$CONTAINER_ID")" = "65532:65532"

echo "container smoke test passed: $IMAGE"
