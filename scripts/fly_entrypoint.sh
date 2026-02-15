#!/usr/bin/env bash
set -euo pipefail

readonly PINCER_BIN="${PINCER_BIN:-/app/pincer}"
readonly PINCER_PORT="${PINCER_PORT:-8080}"
readonly PINCER_HTTP_ADDR="${PINCER_HTTP_ADDR:-127.0.0.1:${PINCER_PORT}}"
readonly PINCER_DB_PATH="${PINCER_DB_PATH:-/data/pincer.db}"

readonly TS_STATE_DIR="${TS_STATE_DIR:-/data/tailscale}"
readonly TS_SOCKET="${TS_SOCKET:-/var/run/tailscale/tailscaled.sock}"
readonly TS_HOSTNAME="${TS_HOSTNAME:-pincer}"
readonly TS_SERVICE_NAME="${TS_SERVICE_NAME:-pincer}"
readonly TS_SERVE_PORT="${TS_SERVE_PORT:-443}"
readonly TS_TARGET_ADDR="${TS_TARGET_ADDR:-127.0.0.1:${PINCER_PORT}}"
readonly TS_TUN_MODE="${TS_TUN_MODE:-userspace-networking}"

if [[ -z "${PINCER_TOKEN_HMAC_KEY:-}" ]]; then
  echo "PINCER_TOKEN_HMAC_KEY is required" >&2
  exit 1
fi

if [[ -z "${TS_AUTHKEY:-}" ]]; then
  echo "TS_AUTHKEY is required" >&2
  exit 1
fi

mkdir -p "${TS_STATE_DIR}" "$(dirname "${TS_SOCKET}")"

tailscaled \
  --tun="${TS_TUN_MODE}" \
  --socket="${TS_SOCKET}" \
  --state="${TS_STATE_DIR}/tailscaled.state" &
tailscaled_pid="$!"

cleanup() {
  if kill -0 "${tailscaled_pid}" >/dev/null 2>&1; then
    kill "${tailscaled_pid}" >/dev/null 2>&1 || true
    wait "${tailscaled_pid}" >/dev/null 2>&1 || true
  fi
}
trap cleanup EXIT INT TERM

for _ in $(seq 1 60); do
  if tailscale --socket="${TS_SOCKET}" version >/dev/null 2>&1; then
    break
  fi
  sleep 1
done

if ! tailscale --socket="${TS_SOCKET}" version >/dev/null 2>&1; then
  echo "tailscaled did not become ready within 60 seconds" >&2
  exit 1
fi

up_args=(
  "--authkey=${TS_AUTHKEY}"
  "--hostname=${TS_HOSTNAME}"
  "--accept-dns=false"
)
if [[ -n "${TS_UP_EXTRA_ARGS:-}" ]]; then
  # shellcheck disable=SC2206
  extra_up_args=(${TS_UP_EXTRA_ARGS})
  up_args+=("${extra_up_args[@]}")
fi

tailscale --socket="${TS_SOCKET}" up "${up_args[@]}"
tailscale --socket="${TS_SOCKET}" serve --service="svc:${TS_SERVICE_NAME}" --https="${TS_SERVE_PORT}" "${TS_TARGET_ADDR}"
tailscale --socket="${TS_SOCKET}" serve status

exec "${PINCER_BIN}" \
  --http-addr="${PINCER_HTTP_ADDR}" \
  --db-path="${PINCER_DB_PATH}" \
  --token-hmac-key="${PINCER_TOKEN_HMAC_KEY}"
