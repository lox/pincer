# Pincer iOS App

This app is now an OpenClaw-focused iOS shell.

Current responsibilities:

- session list and session detail UI
- approvals surface
- Gateway settings
- local persistence while the direct OpenClaw WebSocket client is implemented

## Local build

- `mise run ios-build`
- `mise run ios-run-simulator`

## Runtime config

The app reads:

- `OPENCLAW_IOS_GATEWAY_URL`
- `OPENCLAW_GATEWAY_URL`
- `OPENCLAW_GATEWAY_TOKEN`
- `OPENCLAW_PRIMARY_SESSION_KEY`

Defaults:

- Gateway URL: `ws://127.0.0.1:18789`
- Primary session key: `main`

## Near-term implementation target

Replace the current local persistence shell with:

1. direct Gateway connect/challenge handling
2. device-auth pairing
3. real session list/history/send
4. real approval list/resolve flows
