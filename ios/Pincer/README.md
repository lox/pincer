# Pincer iOS Shell

This folder contains a minimal SwiftUI shell for the MVP slice:

- `Chat` screen (send messages, render timeline)
- `Approvals` screen (list pending approvals, approve action)

Project files:

- `project.yml` (xcodegen spec)
- `Pincer.xcodeproj` (generated Xcode project)

## Usage

1. Trust project tasks once:
   - `mise trust`
2. Set your backend values in `AppConfig.swift`.
3. Run backend:
   - `mise run run`
4. Build from terminal:
   - `mise run ios-build`
5. Open in Xcode:
   - `open ios/Pincer/Pincer.xcodeproj`
6. Launch app and test the flow:
   - Send message in Chat.
   - Open Approvals.
   - Approve pending action.

## Notes

- This is intentionally minimal for Phase 1 MVP.
- It uses a static bearer token for dev (`PINCER_DEV_TOKEN` on backend).
- If build fails with missing iOS platform/runtime, install it from Xcode -> Settings -> Components, then rerun `mise run ios-build`.
