# AGENTS.md

This file defines how coding agents should work in this repository.

## 1. Project intent

Pincer is an iOS-first OpenClaw companion app.

Primary goal:

- build a native operator client for OpenClaw sessions, approvals, and Gateway connectivity.

Current posture:

- no standalone Pincer backend,
- no duplicate control plane,
- no hidden second agent runtime behind the app.

## 2. Read these first

Before making changes, read:

1. `docs/openclaw-pivot-proposal.md`
2. `README.md`
3. `ios/Pincer/README.md`

Treat `docs/openclaw-pivot-proposal.md` as the current architecture direction.

## 3. Product constraints

1. Do not rebuild a second backend/runtime unless the user explicitly asks for it.
2. Stay aligned with OpenClaw's session and approval model.
3. Prefer direct Gateway integration over custom translation layers.
4. Keep the iOS app useful even while the direct Gateway client is still in progress.
5. Preserve a clean path for sessions, approvals, and settings.

## 4. Current vertical slice

The minimum useful slice is:

1. iOS app launches.
2. User can configure OpenClaw Gateway settings.
3. User can manage sessions in the app shell.
4. User can see the approvals surface.
5. Repo builds without the old backend.

## 5. Repository layout

- `ios/Pincer` - SwiftUI app + generated Xcode project
- `ios/PincerTests` - unit tests for app support code
- `ios/PincerUITests` - UI tests
- `docs/openclaw-pivot-proposal.md` - pivot recommendation and migration plan

## 6. Tooling and commands

Use `mise` for routine tasks:

- `mise run ios-generate` - generate Xcode project from `project.yml`
- `mise run ios-build` - build iOS app for simulator (no signing)
- `mise run ios-run-simulator` - build, install, and launch app in iOS Simulator
- `mise run ios-run-device` - build, install, and launch app on a connected device

If `mise` is blocked, run `mise trust` in repo root.

## 7. Engineering workflow

1. Start from a failing test when changing app behavior that is easy to isolate.
2. Implement the smallest slice that moves the OpenClaw client forward.
3. Keep the app buildable after each slice.
4. Prefer replacing backend-coupled seams over layering more compatibility code on top.

## 8. iOS guidance

- Use SwiftUI and async/await.
- Keep iOS deployment baseline at `26.0` unless explicitly changed by the user.
- Preserve a session-first mental model.
- Keep approvals explicit and easy to reach.
- Treat Gateway configuration and connection state as first-class UX.

## 9. Done criteria

A change is only complete when:

1. The app direction remains aligned with OpenClaw.
2. README/docs are updated when behavior changes.
3. The relevant iOS build/test command succeeds, or failure is explained clearly.
