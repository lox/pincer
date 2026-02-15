#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
PROJECT_PATH="${ROOT_DIR}/ios/Pincer/Pincer.xcodeproj"
SCHEME="${PINCER_IOS_SCHEME:-Pincer}"
BUNDLE_ID="${PINCER_IOS_BUNDLE_ID:-com.lox.pincer}"
DEVICE_UDID="${PINCER_IOS_DEVICE_UDID:-${PINCER_IOS_DEVICE:-}}"
CONFIGURATION="${PINCER_IOS_CONFIGURATION:-Debug}"

BASE_URL="${PINCER_BASE_URL:-http://127.0.0.1:8080}"
DEVELOPMENT_TEAM="${PINCER_IOS_DEVELOPMENT_TEAM:-${DEVELOPMENT_TEAM:-}}"
CODE_SIGN_IDENTITY="${PINCER_IOS_CODE_SIGN_IDENTITY:-iPhone Developer}"
PROVISIONING_PROFILE_SPECIFIER="${PINCER_IOS_PROVISIONING_PROFILE_SPECIFIER:-}"

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
    -configuration "${CONFIGURATION}" \
    -destination "id=${DEVICE_UDID}" \
    -showBuildSettings)"

  target="$(printf '%s\n' "${settings}" | awk -F ' = ' '/TARGET_BUILD_DIR/ {print $2; exit}')"
  wrapper="$(printf '%s\n' "${settings}" | awk -F ' = ' '/WRAPPER_NAME/ {print $2; exit}')"

  if [[ -z "${target}" || -z "${wrapper}" ]]; then
    echo "failed to derive app build path" >&2
    exit 1
  fi
  printf '%s/%s\n' "${target}" "${wrapper}"
}

main() {
  require_cmd xcodebuild
  require_cmd xcrun
  require_cmd curl

  if [[ -z "${DEVICE_UDID}" ]]; then
    echo "PINCER_IOS_DEVICE_UDID (or PINCER_IOS_DEVICE) must be set to your physical device UDID." >&2
    exit 1
  fi

  if [[ -z "${DEVELOPMENT_TEAM}" ]]; then
    echo "PINCER_IOS_DEVELOPMENT_TEAM (or DEVELOPMENT_TEAM) must be set for physical-device signing." >&2
    exit 1
  fi

  backend_code="$(curl -sS -o /dev/null -w '%{http_code}' -X POST "${BASE_URL}/pincer.protocol.v1.AuthService/CreatePairingCode" -H 'Content-Type: application/json' -d '{}' || true)"
  if [[ "${backend_code}" == "000" ]]; then
    echo "warning: backend not reachable at ${BASE_URL}" >&2
    echo "warning: run 'mise run dev' before chatting/approvals" >&2
  fi

  local sign_args=(
    CODE_SIGN_STYLE=Automatic
    CODE_SIGNING_REQUIRED=YES
    CODE_SIGNING_ALLOWED=YES
    DEVELOPMENT_TEAM="${DEVELOPMENT_TEAM}"
    CODE_SIGN_IDENTITY="${CODE_SIGN_IDENTITY}"
  )
  if [[ -n "${PROVISIONING_PROFILE_SPECIFIER}" ]]; then
    sign_args+=(PROVISIONING_PROFILE_SPECIFIER="${PROVISIONING_PROFILE_SPECIFIER}")
  fi

  xcodebuild -project "${PROJECT_PATH}" \
    -scheme "${SCHEME}" \
    -configuration "${CONFIGURATION}" \
    -destination "id=${DEVICE_UDID}" \
    "${sign_args[@]}" \
    build

  APP_PATH="$(get_app_path)"
  if [[ ! -d "${APP_PATH}" ]]; then
    echo "app bundle not found at ${APP_PATH}" >&2
    exit 1
  fi

  xcrun devicectl device install app --device "${DEVICE_UDID}" "${APP_PATH}"
  xcrun devicectl device process terminate --device "${DEVICE_UDID}" "${BUNDLE_ID}" >/dev/null 2>&1 || true
  xcrun devicectl device process launch --device "${DEVICE_UDID}" "${BUNDLE_ID}" >/dev/null

  echo "ios device run ok"
  echo "device_udid=${DEVICE_UDID}"
  echo "bundle_id=${BUNDLE_ID}"
  echo "app_path=${APP_PATH}"
  echo "If pairing is not complete, open Settings in-app and enter your base URL or pair flow."
}

main "$@"
