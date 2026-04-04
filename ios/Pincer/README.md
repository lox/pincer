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
- app lifecycle-aware Gateway transport suspend/resume with a foreground thread refresh
- optimistic user send with streamed assistant draft updates
- rich markdown rendering for assistant and system message bodies
- buffered bootstrap gap recovery so the initial chat load does not depend on a later refresh event
- Control UI-style transcript cleanup for metadata wrappers, timestamp/channel prefixes, silent assistant `NO_REPLY`, hidden heartbeat maintenance turns, and historical tool execution rendered as compact timeline items instead of raw tool/file output
- live tool activity cards and `chat.abort` support in the composer
- live observed exec/plugin approvals with `allow-once`, `allow-always`, and `deny` actions on the active Gateway connection

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

1. approval context and richer decision detail threaded into chat/session surfaces
2. richer historical/live timeline presentation, including denser tool summaries and expansion affordances
3. further reconnect hardening on top of the current bootstrap/gap/foreground fix
4. session/presence shell updates without explicit refresh actions
