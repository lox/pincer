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

- a native single-chat-first app shell,
- a conditional session switcher when OpenClaw exposes non-main sessions,
- approvals and settings surfaces,
- Gateway URL/token/session configuration,
- Gateway reachability probing over WebSocket,
- real Gateway auth probing with device identity, signed `connect`, and Keychain-backed device-token persistence.
- real Gateway-backed chat/session list, history, create/delete, send, and abort flows,
- a long-lived authenticated Gateway connection for chat, agent, health, and presence events,
- event-driven assistant drafts and live tool activity in the chat timeline,
- rich markdown rendering for assistant and system message bodies in the chat timeline,
- snapshot-first bootstrap with buffered gap/chat events so the main thread does not open blank and wait for a later self-heal,
- Control UI-style transcript cleanup for metadata wrappers, timestamp/channel prefixes, silent `NO_REPLY` assistant messages, hidden heartbeat maintenance turns, and historical tool execution artifacts rendered as compact timeline items instead of raw transcript spill,
- local approvals placeholders while the approvals transport is still being replaced.

The next implementation slice is the remaining Gateway operator surface:

1. replace local approvals data with real approval listing and resolution,
2. carry presence/health/session updates into the shell without manual refresh affordances,
3. continue hardening reconnect behavior beyond the current bootstrap/gap fix,
4. tighten the historical/live chat timeline presentation for richer message/tool rendering.

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
