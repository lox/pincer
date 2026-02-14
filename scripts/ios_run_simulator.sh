#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
DEVICE="${PINCER_IOS_DEVICE:-iPhone 17 Pro}"
BUNDLE_ID="${PINCER_IOS_BUNDLE_ID:-com.lox.pincer}"
PROJECT_PATH="${ROOT_DIR}/ios/Pincer/Pincer.xcodeproj"
SCHEME="${PINCER_IOS_SCHEME:-Pincer}"
AUTH_TOKEN="${PINCER_AUTH_TOKEN:-}"
BASE_URL="${PINCER_BASE_URL:-http://127.0.0.1:8080}"

require_cmd() {
  local cmd="$1"
  if ! command -v "${cmd}" >/dev/null 2>&1; then
    echo "${cmd} is required but not installed" >&2
    exit 1
  fi
}

get_app_path() {
  local settings target wrapper
  settings="$(xcodebuild \
    -project "${PROJECT_PATH}" \
    -scheme "${SCHEME}" \
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

require_cmd xcodebuild
require_cmd xcrun
require_cmd open
require_cmd curl

cd "${ROOT_DIR}"

backend_code="$(curl -sS -o /dev/null -w '%{http_code}' -X POST "${BASE_URL}/v1/pairing/code" -H 'Content-Type: application/json' -d '{}' || true)"
if [[ "${backend_code}" == "000" ]]; then
  echo "warning: backend not reachable at ${BASE_URL}" >&2
  echo "warning: run 'mise run dev' before using chat/approvals" >&2
fi

xcodebuild -project "${PROJECT_PATH}" \
  -scheme "${SCHEME}" \
  -destination "generic/platform=iOS Simulator" \
  build CODE_SIGNING_ALLOWED=NO >/dev/null

APP_PATH="$(get_app_path)"
if [[ ! -d "${APP_PATH}" ]]; then
  echo "app bundle not found at ${APP_PATH}" >&2
  exit 1
fi

open -a Simulator >/dev/null 2>&1 || true
xcrun simctl boot "${DEVICE}" >/dev/null 2>&1 || true
xcrun simctl bootstatus "${DEVICE}" -b >/dev/null
xcrun simctl install "${DEVICE}" "${APP_PATH}" >/dev/null

if [[ -n "${AUTH_TOKEN}" ]]; then
  xcrun simctl spawn "${DEVICE}" defaults write "${BUNDLE_ID}" PINCER_BEARER_TOKEN -string "${AUTH_TOKEN}" >/dev/null
fi

xcrun simctl launch "${DEVICE}" "${BUNDLE_ID}" >/dev/null

echo "ios run ok"
echo "device=${DEVICE}"
echo "bundle_id=${BUNDLE_ID}"
echo "app_path=${APP_PATH}"
