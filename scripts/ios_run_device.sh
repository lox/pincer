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

normalize_device_token() {
  printf '%s' "$1" | sed -E 's/^[[:space:]]+|[[:space:]]+$//g'
}

show_ios_destinations() {
  xcodebuild -project "${PROJECT_PATH}" -scheme "${SCHEME}" -showdestinations 2>/dev/null \
    | awk '/platform:iOS,/{print}'
}

parse_dest_id() {
  local line="$1"
  printf '%s\n' "${line}" | sed -E 's/.*id:([^,]+).*/\1/'
}

parse_dest_name() {
  local line="$1"
  printf '%s\n' "${line}" | sed -E 's/.*name:(.*) \}$/\1/'
}

is_uuid_like() {
  local value="$1"
  local stripped
  local hex_only

  if [[ -z "${value}" ]]; then
    return 1
  fi

  stripped="$(printf '%s' "${value}" | tr '[:upper:]' '[:lower:]' | tr -d '-')"
  hex_only="$(printf '%s' "${stripped}" | tr -cd '0-9a-f')"

  if [[ "${stripped}" != "${hex_only}" ]]; then
    return 1
  fi

  if [[ "${#stripped}" -lt 8 || "${#stripped}" -gt 40 ]]; then
    return 1
  fi

  return 0
}

resolve_device_udid() {
  local requested="$1"
  requested="$(normalize_device_token "${requested}")"
  if [[ -z "${requested}" ]]; then
    return 1
  fi

  local line name id
  local exact_match=""
  local partial_matches=()
  local requested_lower
  requested_lower="$(printf '%s' "${requested}" | tr '[:upper:]' '[:lower:]')"
  local name_lower

  while IFS= read -r line; do
    [[ -z "${line}" ]] && continue

    id="$(parse_dest_id "${line}")"
    if ! is_uuid_like "${id}"; then
      continue
    fi

    name="$(parse_dest_name "${line}")"
    name="$(normalize_device_token "${name}")"

    if [[ "${requested}" == "${id}" ]]; then
      printf '%s\n' "${id}"
      return 0
    fi

    name_lower="$(printf '%s' "${name}" | tr '[:upper:]' '[:lower:]')"
    if [[ "${requested_lower}" == "${name_lower}" ]]; then
      exact_match="${id}"
    elif [[ "${name_lower}" == *"${requested_lower}"* ]]; then
      partial_matches+=("${id}")
    fi
  done < <(show_ios_destinations)

  if [[ -n "${exact_match}" ]]; then
    printf '%s\n' "${exact_match}"
    return 0
  fi

  if (( ${#partial_matches[@]} == 1 )); then
    printf '%s\n' "${partial_matches[0]}"
    return 0
  fi

  if (( ${#partial_matches[@]} > 1 )); then
    echo "multiple devices match '${requested}'. Specify exact device name or UDID:" >&2
    while IFS= read -r line; do
      id="$(parse_dest_id "${line}")"
      name="$(normalize_device_token "$(parse_dest_name "${line}")")"
      echo "  - ${name} (${id})" >&2
    done < <(show_ios_destinations)
    return 1
  fi

  echo "unable to resolve device '${requested}' from connected iOS devices." >&2
  echo "available devices:" >&2
  while IFS= read -r line; do
    id="$(parse_dest_id "${line}")"
    name="$(normalize_device_token "$(parse_dest_name "${line}")")"
    echo "  - ${name} (${id})" >&2
  done < <(show_ios_destinations)
  return 1
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
  require_cmd awk

  if [[ -z "${DEVICE_UDID}" ]]; then
    echo "PINCER_IOS_DEVICE_UDID (or PINCER_IOS_DEVICE) must be set to your physical device name or Xcode UDID." >&2
    exit 1
  fi

  DEVICE_UDID="$(normalize_device_token "${DEVICE_UDID}")"

  if [[ -z "${DEVELOPMENT_TEAM}" ]]; then
    echo "PINCER_IOS_DEVELOPMENT_TEAM (or DEVELOPMENT_TEAM) must be set for physical-device signing." >&2
    exit 1
  fi

  backend_code="$(curl -sS -o /dev/null -w '%{http_code}' -X POST "${BASE_URL}/pincer.protocol.v1.AuthService/CreatePairingCode" -H 'Content-Type: application/json' -d '{}' || true)"
  if [[ "${backend_code}" == "000" ]]; then
    echo "warning: backend not reachable at ${BASE_URL}" >&2
    echo "warning: run 'mise run dev' before chatting/approvals" >&2
  fi

  resolved_udid=""
  if ! resolved_udid="$(resolve_device_udid "${DEVICE_UDID}")"; then
    if [[ -n "${PINCER_IOS_DEVICE:-}" ]]; then
      device_hint="$(normalize_device_token "${PINCER_IOS_DEVICE}")"
      if [[ -n "${device_hint}" && "${device_hint}" != "${DEVICE_UDID}" ]]; then
        resolved_udid="$(resolve_device_udid "${device_hint}" || true)"
      fi
    fi
  fi

  if [[ -z "${resolved_udid}" ]]; then
    exit 1
  fi

  DEVICE_UDID="${resolved_udid}"

  if ! is_uuid_like "${DEVICE_UDID}"; then
    echo "resolved device id is invalid: ${DEVICE_UDID}" >&2
    exit 1
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
