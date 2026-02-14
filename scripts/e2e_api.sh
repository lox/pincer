#!/usr/bin/env bash
set -euo pipefail

BASE_URL="${PINCER_BASE_URL:-http://127.0.0.1:18080}"
HTTP_ADDR="${PINCER_HTTP_ADDR:-:18080}"
AUTH_TOKEN="${PINCER_AUTH_TOKEN:-}"
START_BACKEND="${PINCER_E2E_START_BACKEND:-1}"

if [[ "${START_BACKEND}" == "1" ]]; then
  PINCER_TMUX_SESSION="${PINCER_TMUX_SESSION:-pincer-backend-e2e}" \
  PINCER_DB_PATH="${PINCER_DB_PATH:-/tmp/pincer-e2e.db}" \
  PINCER_HTTP_ADDR="${HTTP_ADDR}" \
  PINCER_BASE_URL="${BASE_URL}" \
  PINCER_E2E_RESET_DB="${PINCER_E2E_RESET_DB:-1}" \
    "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/backend_up.sh" >/dev/null
fi

PINCER_BASE_URL="${BASE_URL}" \
PINCER_AUTH_TOKEN="${AUTH_TOKEN}" \
PINCER_E2E_MESSAGE="${PINCER_E2E_MESSAGE:-Please run bash command pwd and require approval}" \
  go run ./cmd/pincer-e2e-api
