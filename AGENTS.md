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
2. `docs/ios-ui-plan.md`
3. `mvp.md`
4. `README.md`

Treat `docs/spec.md` as the canonical system contract.

## 3. Non-negotiable invariants

1. LLM output is untrusted.
2. External side effects must follow:
   - `proposed -> approved -> executed -> audited`
3. No silent external writes/sends.
4. Idempotency must gate external execution.
5. Background autonomy is internal-only unless explicitly approved.

## 4. Current MVP vertical slice

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
- `ios/Pincer` - SwiftUI MVP app + generated Xcode project
- `docs/spec.md` - backend and security spec
- `docs/ios-ui-plan.md` - iOS UI/UX plan
- `mvp.md` - end-to-end MVP definition

## 6. Tooling and commands

Use `mise` for all routine tasks:

- `mise run run` - start backend
- `mise run test` - run Go tests
- `mise run fmt` - format Go code
- `mise run tidy` - tidy Go modules
- `mise run ios-generate` - generate Xcode project from `project.yml`
- `mise run ios-build` - build iOS app for simulator (no signing)
- `mise run e2e-api` - run backend API E2E conveyor checks
- `mise run e2e-ios` - run simulator UI + backend E2E checks

If `mise` is blocked, run `mise trust` in repo root.

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

## 11. Reproducible local E2E flow (tmux + API + iOS)

This repo includes a repeatable MVP E2E path. Use it before/after changing approval flow code.

### 11.1 Prerequisites

- `tmux`
- `mise`
- `curl`
- `jq`
- Xcode + iOS Simulator (for iOS checks)

### 11.2 Start/stop backend in tmux

Use the provided tasks:

- `mise run backend-up`
- `mise run backend-down`
- `mise run e2e-ios`

Behavior of `backend-up`:

- creates tmux session `pincer-backend` (or `PINCER_TMUX_SESSION`)
- starts backend via `mise run run`
- waits for `GET /v1/audit` to return `200`
- defaults to a clean DB at `/tmp/pincer-e2e.db` each run

Useful tmux inspection commands:

- `tmux ls`
- `tmux capture-pane -pt pincer-backend:0 | tail -n 80`
- `tmux attach -t pincer-backend`

### 11.3 Automated MVP API E2E

Run:

- `mise run e2e-api`

This validates:

1. `POST /v1/chat/threads`
2. `POST /v1/chat/threads/{thread_id}/messages`
3. `GET /v1/approvals?status=pending`
4. `POST /v1/approvals/{action_id}/approve`
5. `GET /v1/approvals?status=executed`
6. `GET /v1/audit` includes:
   - `action_proposed`
   - `action_approved`
   - `action_executed`

Expected success output includes:

- `e2e ok`
- `thread_id=...`
- `action_id=...`

### 11.4 iOS verification flow (manual or agent-driven)

Build app:

- `mise run ios-build`

App config defaults in `ios/Pincer/AppConfig.swift`:

- `baseURL = http://127.0.0.1:8080`
- `bearerToken = dev-token`

Manual check path in app:

1. Open Chat
2. Send message
3. Open Approvals
4. Approve pending action
5. Confirm action disappears from pending list
6. Confirm backend state with:
   - `curl -sS 'http://127.0.0.1:8080/v1/approvals?status=executed' -H 'Authorization: Bearer dev-token' | jq`
   - `curl -sS 'http://127.0.0.1:8080/v1/audit' -H 'Authorization: Bearer dev-token' | jq`

Agent-driven option:

- use the `agent-device` skill and run via `npx -y agent-device ...`
- if simulator UI interaction is flaky, treat `mise run e2e-api` as the required automated gate and run iOS checks manually
- primary scripted command is `mise run e2e-ios` (it boots backend, installs app, drives UI actions, and verifies backend state)

### 11.5 Environment overrides

The scripts honor:

- `PINCER_TMUX_SESSION`
- `PINCER_HTTP_ADDR`
- `PINCER_BASE_URL`
- `PINCER_DB_PATH`
- `PINCER_DEV_TOKEN`
- `PINCER_E2E_RESET_DB` (`1` default, set `0` to keep DB)
