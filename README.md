<h1>
  <img src="assets/claw-640px-transparent.png" alt="" width="80" style="vertical-align: middle;" />
  Pincer
</h1>

Pincer is now an iOS-first OpenClaw companion app.

The old standalone Go backend/runtime has been removed from the product direction. The repository now focuses on:

1. native iPhone operator UX,
2. OpenClaw sessions as the core chat model,
3. approvals as a first-class mobile surface,
4. direct OpenClaw Gateway integration instead of a separate Pincer control plane.

## Current state

The iOS app currently provides:

- a native session-based app shell,
- approvals and settings surfaces,
- local session persistence while the transport layer is replaced,
- Gateway URL/token/session configuration,
- Gateway reachability probing over WebSocket.

The next implementation slice is the direct OpenClaw Gateway client:

1. connect/challenge handling,
2. device-auth pairing,
3. real session list/history/send,
4. approval listing and resolution.

## Local development

- `mise run ios-build`
- `mise run ios-run-simulator`
- `mise run ios-run-device`

Useful overrides:

- `OPENCLAW_IOS_GATEWAY_URL`
- `OPENCLAW_GATEWAY_URL`
- `OPENCLAW_GATEWAY_TOKEN`
- `OPENCLAW_PRIMARY_SESSION_KEY`

Default Gateway URL in the app is `ws://127.0.0.1:18789`.

## Docs

- `docs/openclaw-pivot-proposal.md` - architecture options, recommendation, and migration path
- `ios/Pincer/README.md` - app-specific development notes
- `AGENTS.md` - repository-specific instructions
