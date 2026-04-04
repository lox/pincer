# Pincer iOS App

This app is now an OpenClaw-focused iOS shell.

Current responsibilities:

- single-chat-first UI for the primary OpenClaw session
- conditional session switcher for non-main sessions
- approvals surface
- Gateway settings
- real Gateway auth probing with stored device identity and device token reuse
- real Gateway-backed session list/history/create/delete/send requests
- long-lived authenticated Gateway transport for chat + agent events
- optimistic user send with streamed assistant draft updates
- Control UI-style transcript cleanup for metadata wrappers, timestamp/channel prefixes, silent assistant `NO_REPLY`, and hidden tool execution artifacts
- live tool activity cards and `chat.abort` support in the composer
- local approvals placeholders while approval transport is still being replaced

## Local build

- `mise run ios-build`
- `mise run ios-run-simulator`

## Runtime config

The app reads:

- `OPENCLAW_IOS_GATEWAY_URL`
- `OPENCLAW_GATEWAY_URL`
- `OPENCLAW_GATEWAY_TOKEN`
- `OPENCLAW_PRIMARY_SESSION_KEY`

Gateway secrets are persisted in Keychain. If `OPENCLAW_GATEWAY_TOKEN` is injected via
simulator defaults for local development, the app migrates it into Keychain on first read
and clears the plain `UserDefaults` copy.

The app also stores:

- a stable Ed25519 device identity for signed Gateway `connect` requests
- the issued per-device Gateway auth token returned in `hello-ok.auth.deviceToken`

Defaults:

- Gateway URL: `ws://127.0.0.1:18789`
- Primary session key: `main`

## Near-term implementation target

Extend the current direct Gateway shell with:

1. real approval list/resolve flows
2. tighter reconnect and gap-recovery behavior
3. richer timeline rendering for streamed assistant/tool output
4. session/presence shell updates without explicit refresh actions
