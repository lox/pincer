#!/usr/bin/env bash
set -euo pipefail

SESSION="${PINCER_TMUX_SESSION:-pincer-backend}"
DB_PATH="${PINCER_DB_PATH:-/tmp/pincer-e2e.db}"
HTTP_ADDR="${PINCER_HTTP_ADDR:-:8080}"
BASE_URL="${PINCER_BASE_URL:-http://127.0.0.1:8080}"
DEV_TOKEN="${PINCER_DEV_TOKEN:-dev-token}"
RESET_DB="${PINCER_E2E_RESET_DB:-1}"
ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

if ! command -v tmux >/dev/null 2>&1; then
  echo "tmux is required but not installed" >&2
  exit 1
fi

if tmux has-session -t "${SESSION}" 2>/dev/null; then
  tmux kill-session -t "${SESSION}"
fi

if [[ "${RESET_DB}" == "1" ]]; then
  rm -f "${DB_PATH}" "${DB_PATH}-shm" "${DB_PATH}-wal"
fi

tmux new-session -d -s "${SESSION}" \
  "cd '${ROOT_DIR}' && PINCER_HTTP_ADDR='${HTTP_ADDR}' PINCER_DB_PATH='${DB_PATH}' PINCER_DEV_TOKEN='${DEV_TOKEN}' mise run run"

for _ in $(seq 1 30); do
  code="$(curl -s -o /dev/null -w "%{http_code}" "${BASE_URL}/v1/audit" -H "Authorization: Bearer ${DEV_TOKEN}" || true)"
  if [[ "${code}" == "200" ]]; then
    echo "pincer backend ready in tmux session '${SESSION}'"
    exit 0
  fi
  sleep 1
done

echo "backend did not become ready within 30s" >&2
tmux capture-pane -pt "${SESSION}:0" | tail -n 80 >&2
exit 1
