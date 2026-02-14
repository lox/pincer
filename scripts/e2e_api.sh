#!/usr/bin/env bash
set -euo pipefail

BASE_URL="${PINCER_BASE_URL:-http://127.0.0.1:8080}"
AUTH_TOKEN="${PINCER_AUTH_TOKEN:-}"
AUTH_HEADER=""
START_BACKEND="${PINCER_E2E_START_BACKEND:-1}"

if ! command -v jq >/dev/null 2>&1; then
  echo "jq is required but not installed" >&2
  exit 1
fi

if ! command -v curl >/dev/null 2>&1; then
  echo "curl is required but not installed" >&2
  exit 1
fi

if [[ "${START_BACKEND}" == "1" ]]; then
  "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/backend_up.sh" >/dev/null
fi

bootstrap_auth_token() {
  if [[ -n "${AUTH_TOKEN}" ]]; then
    AUTH_HEADER="Authorization: Bearer ${AUTH_TOKEN}"
    return 0
  fi

  pairing_json="$(curl -sS -X POST "${BASE_URL}/v1/pairing/code" \
    -H "Content-Type: application/json" \
    -d '{}')"
  pairing_code="$(printf '%s' "${pairing_json}" | jq -r '.code')"
  if [[ -z "${pairing_code}" || "${pairing_code}" == "null" ]]; then
    echo "failed to create pairing code: ${pairing_json}" >&2
    exit 1
  fi

  bind_json="$(curl -sS -X POST "${BASE_URL}/v1/pairing/bind" \
    -H "Content-Type: application/json" \
    -d "{\"code\":\"${pairing_code}\",\"device_name\":\"e2e-api\"}")"
  AUTH_TOKEN="$(printf '%s' "${bind_json}" | jq -r '.token')"
  if [[ -z "${AUTH_TOKEN}" || "${AUTH_TOKEN}" == "null" ]]; then
    echo "failed to bind pairing code: ${bind_json}" >&2
    exit 1
  fi

  AUTH_HEADER="Authorization: Bearer ${AUTH_TOKEN}"
}

bootstrap_auth_token

thread_json="$(curl -sS -X POST "${BASE_URL}/v1/chat/threads" \
  -H "${AUTH_HEADER}" \
  -H "Content-Type: application/json" \
  -d '{}')"
thread_id="$(printf '%s' "${thread_json}" | jq -r '.thread_id')"

if [[ -z "${thread_id}" || "${thread_id}" == "null" ]]; then
  echo "failed to create thread: ${thread_json}" >&2
  exit 1
fi

message_json="$(curl -sS -X POST "${BASE_URL}/v1/chat/threads/${thread_id}/messages" \
  -H "${AUTH_HEADER}" \
  -H "Content-Type: application/json" \
  -d '{"content":"Run e2e check"}')"
assistant_message="$(printf '%s' "${message_json}" | jq -r '.assistant_message')"

if [[ -z "${assistant_message}" || "${assistant_message}" == "null" ]]; then
  echo "failed to send message: ${message_json}" >&2
  exit 1
fi

pending_json="$(curl -sS "${BASE_URL}/v1/approvals?status=pending" -H "${AUTH_HEADER}")"
pending_count="$(printf '%s' "${pending_json}" | jq -r '.items | length')"
action_id="$(printf '%s' "${pending_json}" | jq -r '.items[0].action_id')"

if [[ "${pending_count}" -lt 1 || -z "${action_id}" || "${action_id}" == "null" ]]; then
  echo "expected at least one pending approval: ${pending_json}" >&2
  exit 1
fi

approve_http_code="$(curl -sS -o /tmp/pincer-e2e-approve.json -w "%{http_code}" \
  -X POST "${BASE_URL}/v1/approvals/${action_id}/approve" \
  -H "${AUTH_HEADER}" \
  -H "Content-Type: application/json" \
  -d '{}')"

if [[ "${approve_http_code}" != "200" ]]; then
  echo "approval failed with status ${approve_http_code}" >&2
  cat /tmp/pincer-e2e-approve.json >&2
  exit 1
fi

executed_json="$(curl -sS "${BASE_URL}/v1/approvals?status=executed" -H "${AUTH_HEADER}")"
executed_match_count="0"
for _ in $(seq 1 30); do
  executed_json="$(curl -sS "${BASE_URL}/v1/approvals?status=executed" -H "${AUTH_HEADER}")"
  executed_match_count="$(printf '%s' "${executed_json}" | jq -r --arg action_id "${action_id}" '[.items[] | select(.action_id == $action_id)] | length')"
  if [[ "${executed_match_count}" == "1" ]]; then
    break
  fi
  sleep 1
done

if [[ "${executed_match_count}" != "1" ]]; then
  echo "expected action ${action_id} in executed approvals: ${executed_json}" >&2
  exit 1
fi

audit_json="$(curl -sS "${BASE_URL}/v1/audit" -H "${AUTH_HEADER}")"
for event in action_proposed action_approved action_executed; do
  event_count="$(printf '%s' "${audit_json}" | jq -r --arg action_id "${action_id}" --arg event "${event}" '[.items[] | select(.entity_id == $action_id and .event_type == $event)] | length')"
  if [[ "${event_count}" != "1" ]]; then
    echo "missing audit event '${event}' for action ${action_id}" >&2
    exit 1
  fi
done

echo "e2e ok"
echo "thread_id=${thread_id}"
echo "action_id=${action_id}"
echo "assistant_message=${assistant_message}"
