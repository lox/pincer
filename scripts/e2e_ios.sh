#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
DEVICE="${PINCER_IOS_DEVICE:-iPhone 17 Pro}"
BUNDLE_ID="${PINCER_IOS_BUNDLE_ID:-com.lox.pincer}"
SESSION="${PINCER_AGENT_SESSION:-pincer-e2e-ios}"
BASE_URL="${PINCER_BASE_URL:-http://127.0.0.1:8080}"
DEV_TOKEN="${PINCER_DEV_TOKEN:-dev-token}"
AUTH_HEADER="Authorization: Bearer ${DEV_TOKEN}"
MESSAGE_TEXT="${PINCER_E2E_MESSAGE:-Run iOS e2e flow $(date +%s)}"
SCREENSHOT_PATH="${PINCER_E2E_SCREENSHOT:-/tmp/pincer-e2e-ios.png}"

require_cmd() {
  local cmd="$1"
  if ! command -v "${cmd}" >/dev/null 2>&1; then
    echo "${cmd} is required but not installed" >&2
    exit 1
  fi
}

clear_agent_device_sessions() {
  local sessions_json names name
  sessions_json="$(npx -y agent-device session list --json 2>/dev/null || true)"
  names="$(printf '%s' "${sessions_json}" | jq -r '.data.sessions[]?.name')"
  while IFS= read -r name; do
    if [[ -n "${name}" ]]; then
      npx -y agent-device close --session "${name}" >/dev/null 2>&1 || true
    fi
  done <<< "${names}"
}

get_app_path() {
  local settings target wrapper
  settings="$(xcodebuild \
    -project "${ROOT_DIR}/ios/Pincer/Pincer.xcodeproj" \
    -scheme Pincer \
    -destination "generic/platform=iOS Simulator" \
    -showBuildSettings)"

  target="$(printf '%s\n' "${settings}" | awk -F ' = ' '/TARGET_BUILD_DIR/ {print $2; exit}')"
  wrapper="$(printf '%s\n' "${settings}" | awk -F ' = ' '/WRAPPER_NAME/ {print $2; exit}')"

  if [[ -z "${target}" || -z "${wrapper}" ]]; then
    echo "failed to derive app build path" >&2
    exit 1
  fi
  printf '%s/%s\n' "${target}" "${wrapper}"
}

wait_for_pending_action() {
  local attempts=30
  local pending_json pending_count
  while (( attempts > 0 )); do
    pending_json="$(curl -sS "${BASE_URL}/v1/approvals?status=pending" -H "${AUTH_HEADER}")"
    pending_count="$(printf '%s' "${pending_json}" | jq -r '.items | length')"
    if [[ "${pending_count}" -gt 0 ]]; then
      printf '%s' "${pending_json}" | jq -r '.items[0].action_id'
      return 0
    fi
    attempts=$((attempts - 1))
    sleep 1
  done
  return 1
}

wait_for_executed_action() {
  local action_id="$1"
  local attempts=30
  local executed_json match_count
  while (( attempts > 0 )); do
    executed_json="$(curl -sS "${BASE_URL}/v1/approvals?status=executed" -H "${AUTH_HEADER}")"
    match_count="$(printf '%s' "${executed_json}" | jq -r --arg action_id "${action_id}" '[.items[] | select(.action_id == $action_id)] | length')"
    if [[ "${match_count}" == "1" ]]; then
      return 0
    fi
    attempts=$((attempts - 1))
    sleep 1
  done
  return 1
}

require_cmd jq
require_cmd curl
require_cmd xcrun
require_cmd xcodebuild
require_cmd npx

cd "${ROOT_DIR}"
scripts/backend_up.sh >/dev/null
mise run ios-build >/dev/null

APP_PATH="$(get_app_path)"
if [[ ! -d "${APP_PATH}" ]]; then
  echo "app bundle not found at ${APP_PATH}" >&2
  exit 1
fi

xcrun simctl boot "${DEVICE}" >/dev/null 2>&1 || true
xcrun simctl bootstatus "${DEVICE}" -b >/dev/null
xcrun simctl uninstall "${DEVICE}" "${BUNDLE_ID}" >/dev/null 2>&1 || true
xcrun simctl install "${DEVICE}" "${APP_PATH}" >/dev/null

clear_agent_device_sessions
npx -y agent-device open Pincer --platform ios --device "${DEVICE}" --session "${SESSION}" --relaunch >/dev/null
npx -y agent-device wait 'id="chat_input"' 15000 --session "${SESSION}" >/dev/null
npx -y agent-device fill 'id="chat_input"' "${MESSAGE_TEXT}" --session "${SESSION}" >/dev/null
npx -y agent-device click 'id="chat_send_button"' --session "${SESSION}" >/dev/null

ACTION_ID="$(wait_for_pending_action || true)"
if [[ -z "${ACTION_ID}" ]]; then
  echo "no pending action observed after chat send" >&2
  exit 1
fi

# The keyboard can block tab interactions after send; relaunch to restore a clean UI state.
npx -y agent-device open Pincer --platform ios --device "${DEVICE}" --session "${SESSION}" --relaunch >/dev/null
npx -y agent-device wait 'id="chat_input"' 15000 --session "${SESSION}" >/dev/null

if ! npx -y agent-device click 'id="tab_approvals"' --session "${SESSION}" >/dev/null 2>&1; then
  npx -y agent-device find label "Approvals" click --session "${SESSION}" >/dev/null
fi
npx -y agent-device wait 'id="approvals_heading"' 10000 --session "${SESSION}" >/dev/null || true

if ! npx -y agent-device click 'id="approval_approve_first"' --session "${SESSION}" >/dev/null 2>&1; then
  if ! npx -y agent-device click "id=\"approval_approve_${ACTION_ID}\"" --session "${SESSION}" >/dev/null 2>&1; then
    npx -y agent-device find label "Approve" click --session "${SESSION}" >/dev/null
  fi
fi

if ! wait_for_executed_action "${ACTION_ID}"; then
  echo "action ${ACTION_ID} was not observed in executed approvals" >&2
  exit 1
fi

AUDIT_JSON="$(curl -sS "${BASE_URL}/v1/audit" -H "${AUTH_HEADER}")"
for event in action_proposed action_approved action_executed; do
  EVENT_COUNT="$(printf '%s' "${AUDIT_JSON}" | jq -r --arg action_id "${ACTION_ID}" --arg event "${event}" '[.items[] | select(.entity_id == $action_id and .event_type == $event)] | length')"
  if [[ "${EVENT_COUNT}" != "1" ]]; then
    echo "missing audit event '${event}' for action ${ACTION_ID}" >&2
    exit 1
  fi
done

npx -y agent-device screenshot "${SCREENSHOT_PATH}" --session "${SESSION}" >/dev/null

echo "e2e ios ok"
echo "action_id=${ACTION_ID}"
echo "screenshot=${SCREENSHOT_PATH}"
