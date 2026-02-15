# Pincer iOS Shell

This folder contains the SwiftUI control app:

- `Chat` screen (send messages, render timeline)
- `Approvals` screen (list pending approvals, approve action)
- `Settings` screen (list paired devices, revoke session)

Project files:

- `project.yml` (xcodegen spec)
- `Pincer.xcodeproj` (generated Xcode project)

## Usage

1. Trust project tasks once:
   - `mise trust`
2. Run backend:
   - `mise run run`
3. Open app Settings and set `Backend -> Address` (for device testing, use your Mac's LAN URL).
4. Build from terminal:
   - `mise run ios-build`
5. Or run on a physical device:
   - Set `PINCER_IOS_DEVICE_UDID` and your signing team:
     - `export PINCER_IOS_DEVICE_UDID=<your-device-udid>`
     - `export PINCER_IOS_DEVELOPMENT_TEAM=A49UDW9T42`
   - Run:
     - `mise run ios-run-device`
6. Open in Xcode:
   - `open ios/Pincer/Pincer.xcodeproj`
7. Launch app and test the flow:
   - Send message in Chat.
   - Open Approvals.
   - Approve pending action.

## Notes

- This is intentionally minimal for current Phase 1 implementation.
- RPC clients and protobuf models are generated into `ios/Pincer/Generated` from `proto/pincer/protocol/v1/protocol.proto` (`go run github.com/bufbuild/buf/cmd/buf@v1.57.0 generate`).
- The app uses opaque bearer tokens from pairing (`AuthService.CreatePairingCode` + `AuthService.BindPairingCode`).
- Token is stored in `UserDefaults` under `PINCER_BEARER_TOKEN` for simulator/dev flows.
- Backend base URL is stored in `UserDefaults` under `PINCER_BASE_URL`.
- If build fails with missing iOS platform/runtime, install it from Xcode -> Settings -> Components, then rerun `mise run ios-build`.
