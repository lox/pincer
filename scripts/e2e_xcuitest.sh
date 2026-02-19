#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

# E2E uses isolated defaults that differ from dev settings.
# Hardcode these to avoid inheriting mise's dev-mode env values.
BASE_URL="http://127.0.0.1:18080"
HTTP_ADDR=":18080"
SESSION="pincer-backend-e2e"
HMAC_KEY="pincer-dev-token-hmac-key-change-me"
DB_DIR="$(mktemp -d)"
DB_PATH="${DB_DIR}/pincer.db"

# Resolve full path to go binary so the tmux session (which starts a
# fresh shell without mise-managed PATH) can find it.
GO_BIN="$(command -v go 2>/dev/null || true)"
if [[ -z "${GO_BIN}" ]]; then
  echo "go is required but not found in PATH" >&2
  exit 1
fi

cleanup() {
  tmux kill-session -t "${SESSION}" 2>/dev/null || true
  rm -rf "${DB_DIR}"
}
trap cleanup EXIT

# --- Start backend in tmux ---------------------------------------------------
tmux kill-session -t "${SESSION}" 2>/dev/null || true
tmux new-session -d -s "${SESSION}" \
  "cd '${ROOT_DIR}' && PINCER_HTTP_ADDR='${HTTP_ADDR}' PINCER_DB_PATH='${DB_PATH}' PINCER_TOKEN_HMAC_KEY='${HMAC_KEY}' '${GO_BIN}' run ./cmd/pincer serve"

for _ in $(seq 1 30); do
  code="$(curl -s -o /dev/null -w "%{http_code}" -X POST "${BASE_URL}/pincer.protocol.v1.AuthService/CreatePairingCode" -H "Content-Type: application/json" -d '{}' || true)"
  if [[ "${code}" == "200" || "${code}" == "401" ]]; then
    break
  fi
  sleep 1
done

if [[ "${code}" != "200" && "${code}" != "401" ]]; then
  echo "backend did not become ready within 30s" >&2
  tmux capture-pane -pt "${SESSION}:0" 2>/dev/null | tail -n 40 >&2 || true
  exit 1
fi

# --- Configure simulator app to use E2E backend ------------------------------
# xcodebuild does not forward custom env vars to the XCUITest runner,
# so we write directly to the app's sandboxed UserDefaults plist.
BUNDLE_ID="com.lox.pincer"
APP_DATA="$(xcrun simctl get_app_container booted "${BUNDLE_ID}" data 2>/dev/null || true)"
if [[ -n "${APP_DATA}" ]]; then
  PLIST="${APP_DATA}/Library/Preferences/${BUNDLE_ID}.plist"
  plutil -replace PINCER_BASE_URL -string "${BASE_URL}" "${PLIST}" 2>/dev/null || \
    plutil -insert PINCER_BASE_URL -string "${BASE_URL}" "${PLIST}" 2>/dev/null || true
  plutil -remove PINCER_BEARER_TOKEN "${PLIST}" 2>/dev/null || true
else
  xcrun simctl spawn booted defaults write "${BUNDLE_ID}" PINCER_BASE_URL "${BASE_URL}"
  xcrun simctl spawn booted defaults delete "${BUNDLE_ID}" PINCER_BEARER_TOKEN 2>/dev/null || true
fi

# --- Run XCUITests ------------------------------------------------------------
xcodebuild test \
    -project "${ROOT_DIR}/ios/Pincer/Pincer.xcodeproj" \
    -scheme Pincer \
    -destination 'platform=iOS Simulator,name=iPhone 17 Pro' \
    -only-testing:PincerUITests \
    CODE_SIGNING_ALLOWED=NO
