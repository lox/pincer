# AGENTS.md

This file defines how coding agents should work in this repository.

## 1. Project intent

Pincer is a security-first autonomous assistant with:

- Go backend (`github.com/lox/pincer`)
- iOS control app (SwiftUI)
- approval-gated external actions

Primary goal: autonomy within strict safety constraints.

## 2. Read these first

Before making changes, read:

1. `docs/spec.md`
2. `docs/protocol.md`
3. `docs/ios-ui-plan.md`
4. `PLAN.md`
5. `README.md`

Treat `docs/spec.md` as the canonical system contract and `docs/protocol.md` as the canonical ConnectRPC wire contract.

## 3. Non-negotiable invariants

1. LLM output is untrusted.
2. External side effects must follow:
   - `proposed -> approved -> executed -> audited`
3. No silent external writes/sends.
4. Idempotency must gate external execution.
5. Background autonomy is internal-only unless explicitly approved.

## 4. Current vertical slice

The minimum end-to-end slice is:

1. iOS Chat sends message.
2. Backend creates assistant reply + `PENDING` proposed action.
3. iOS Approvals lists action.
4. User approves.
5. Backend executes + audits.

Keep this slice working while iterating.

## 5. Repository layout

- `cmd/pincer` - server entrypoint
- `internal/server` - HTTP handlers, persistence, approval flow
- `internal/server/eval_test.go` - eval tests (real LLM, `//go:build eval`)
- `ios/Pincer` - SwiftUI app + generated Xcode project
- `ios/PincerUITests` - XCUITest E2E for iOS UI
- `docs/spec.md` - backend and security spec
- `docs/protocol.md` - ConnectRPC/protobuf protocol and streaming contract
- `docs/ios-ui-plan.md` - iOS UI/UX plan
- `PLAN.md` - phased implementation plan

## 6. Tooling and commands

Use `mise` for all routine tasks:

- `mise run run` - start backend
- `mise run test` - run Go tests
- `mise run fmt` - format Go code
- `mise run tidy` - tidy Go modules
- `mise run ios-generate` - generate Xcode project from `project.yml`
- `mise run ios-build` - build iOS app for simulator (no signing)
- `mise run ios-run-simulator` - build, install, and launch app in iOS Simulator
- `mise run eval` - run eval tests with real LLM (requires `OPENROUTER_API_KEY`)
- `mise run e2e-api` - alias for `mise run eval`
- `mise run e2e-xcuitest` - run XCUITest E2E against a fresh backend

If `mise` is blocked, run `mise trust` in repo root.

When using `fly logs`, always pass `--no-tail` to avoid streaming forever.

## 7. Engineering workflow

1. Start from a failing test for backend behavior changes.
2. Implement minimal code to pass.
3. Refactor with tests green.
4. Keep changes scoped to one logical slice.

For docs-only changes, keep specs and README aligned.

## 8. iOS guidance

- Use SwiftUI and async/await.
- Keep iOS deployment baseline at `26.0` unless explicitly changed by the user.
- Prefer deterministic backend-rendered approval text.
- Keep safety surfaces explicit:
  - approval state
  - expiry visibility
  - action status transitions

## 9. Phase 1 boundaries

Do not introduce in Phase 1 unless explicitly requested:

- multi-tenant runtime
- domain-wide delegation
- arbitrary subprocess execution
- policy-bypass automation

Future option (deferred): Python skill runtime isolation via `pydantic/monty`.

## 10. Done criteria for feature changes

A change is only complete when:

1. It preserves the security invariants above.
2. It is reflected in docs if behavior changed.
3. The relevant `mise` test/build command succeeds.

Before committing, always check whether `README.md`, `PLAN.md`, `docs/spec.md`, or `docs/protocol.md` need updating to reflect the change. Do not commit code changes that alter user-facing behavior or add/remove features without updating the relevant docs in the same commit.

## 11. Reproducible local E2E flow (tmux + API + iOS)

This repo includes a repeatable E2E path. Use it before/after changing approval flow code.

### 11.1 Prerequisites

- `tmux`
- `mise`
- `curl`
- `jq`
- Xcode + iOS Simulator (for iOS checks)

### 11.2 E2E backend lifecycle

The `e2e_xcuitest.sh` script starts its own isolated backend:

- creates tmux session `pincer-backend-e2e`
- starts backend via `go run ./cmd/pincer` on `:18080`
- uses a temp directory for the DB (cleaned up on exit)
- waits for `POST /pincer.protocol.v1.AuthService/CreatePairingCode` to return `200` (or `401`)

Values are hardcoded in each script to avoid inheriting mise's dev-mode env.

Useful tmux inspection commands:

- `tmux ls`
- `tmux capture-pane -pt pincer-backend-e2e:0 | tail -n 80`

### 11.3 Eval tests (real LLM)

Run:

- `mise run eval`

This is a Go test (`internal/server/eval_test.go`) with build tag `eval`. It spins up an in-process `httptest.NewServer` with the real OpenAI planner — no tmux backend needed.

Requires `OPENROUTER_API_KEY`. Skips automatically if not set.

Validates the full conveyor:

1. Bootstrap auth via pairing
2. Create thread
3. Send turn (real LLM response)
4. Assert a tool proposal was returned
5. Approve action
6. Wait for execution
7. Verify `action_proposed`, `action_approved`, `action_executed` audit events

Run directly with Go:

- `go test ./internal/server -tags=eval -run TestEval -count=1 -timeout 120s -v`

`mise run e2e-api` is an alias that invokes the same test.

### 11.4 iOS verification flow (manual or agent-driven)

Build app:

- `mise run ios-build`

App config defaults in `ios/Pincer/AppConfig.swift`:

- `baseURL = http://127.0.0.1:8080`
- `bearerToken` loads from `UserDefaults` key `PINCER_BEARER_TOKEN`
- if no token exists, client auto-pairs via `AuthService.CreatePairingCode` and `AuthService.BindPairingCode`

Manual check path in app:

1. Open Chat
2. Send message
3. Open Approvals
4. Approve pending action
5. Confirm action disappears from pending list
6. Fetch a bearer token (if needed), then confirm backend state:
   - `PAIRING_CODE="$(curl -sS -X POST 'http://127.0.0.1:8080/pincer.protocol.v1.AuthService/CreatePairingCode' -H 'Content-Type: application/json' -d '{}' | jq -r '.code')"`
   - `TOKEN="$(curl -sS -X POST 'http://127.0.0.1:8080/pincer.protocol.v1.AuthService/BindPairingCode' -H 'Content-Type: application/json' -d "{\"code\":\"${PAIRING_CODE}\",\"deviceName\":\"manual-check\"}" | jq -r '.token')"`
   - `curl -sS -X POST 'http://127.0.0.1:8080/pincer.protocol.v1.ApprovalsService/ListApprovals' -H "Authorization: Bearer ${TOKEN}" -H 'Content-Type: application/json' -d '{"status":"EXECUTED"}' | jq`
   - `curl -sS -X POST 'http://127.0.0.1:8080/pincer.protocol.v1.SystemService/ListAudit' -H "Authorization: Bearer ${TOKEN}" -H 'Content-Type: application/json' -d '{}' | jq`

### 11.5 XCUITest (native Xcode UI testing)

Run:

- `mise run e2e-xcuitest`

This runs the `PincerUITests` target via `xcodebuild test`. The script starts its own backend, configures the simulator app's UserDefaults to point at it, and cleans up on exit. No manual backend setup required.

Test flow: launch app → send chat message → switch to Approvals tab → approve first pending action → verify it disappears.

XCUITests use the accessibility identifiers defined in `ios/Pincer/ContentView.swift` (`A11y` enum).

### 11.6 Environment overrides

The scripts honor:

- `PINCER_AUTH_TOKEN`
- `OPENROUTER_API_KEY` (required for eval tests)
- `PINCER_MODEL_PRIMARY` (default `anthropic/claude-opus-4.6`)
- `PINCER_MODEL_FALLBACK`
