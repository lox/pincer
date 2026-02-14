<p align="left">
  <img src="assets/pincer.png" alt="Pincer logo" width="180" />
</p>

# Pincer

Pincer is a security-first autonomous assistant with a Go backend and an iOS control app.
It is designed for high autonomy with strict control over side effects.

Inspired by [OpenClaw](https://github.com/openclaw/openclaw) and [Nanobot](https://github.com/HKUDS/nanobot).

## Core idea

- The model is untrusted.
- The model may propose actions.
- Trusted code evaluates policy and executes actions.
- External side effects are never silent.

All external actions follow:

`proposed -> approved -> executed -> audited`

- Triggered turns are executed as bounded planner/tool loops, with each tool result written back into context before the final response.

## Why this architecture

Pincer is explicitly designed to mitigate Simon Willison's ["the lethal trifecta"](https://simonwillison.net/2025/Jun/16/the-lethal-trifecta/):

1. Planner and executor are separated.
2. Tool arguments are schema-validated by trusted code.
3. External writes/sends require explicit approval.

## High-level architecture

```text
iOS App
   |
   v
Go Backend (single binary)
   |- Trigger queue (chat/messages, jobs, wakeups, callbacks)
   |- Policy Engine
   |- Approval Queue
   |- Turn Orchestrator (planner -> bounded tool loop)
   |- Action Executor
   |- Job Runner + Scheduler + Wakeups
   |- Tool Registry
   |- SQLite (state + audit)
   |- Provider Client (OpenRouter/OpenAI-compatible)
```

## Local end-to-end

- `mise run dev`
- `mise run reset-db`
- `mise run ios-reset-token`
- `mise run ios-run` (manual simulator launch path)
- `mise run e2e-api`
- `mise run e2e-ios`

Useful overrides:

- `PINCER_BASE_URL`
- `PINCER_DB_PATH`
- `PINCER_AUTH_TOKEN`
- `PINCER_TOKEN_HMAC_KEY`
- `PINCER_E2E_RESET_DB=0`

Database/session defaults:

- `mise run dev` uses `./pincer.db` by default and `PINCER_TOKEN_HMAC_KEY='pincer-dev-token-hmac-key-change-me'`.
- `mise run reset-db` clears `./pincer.db` and associated SQLite journal files.
- `mise run e2e-api` and `mise run e2e-ios` use `/tmp/pincer-e2e.db` in tmux session `pincer-backend-e2e` on `http://127.0.0.1:18080` (reset each run by default).

Backend runtime config is now CLI+env via `kong`:

- `go run ./cmd/pincer --help`
- `OPENROUTER_API_KEY` (legacy fallback: `PINCER_OPENROUTER_API_KEY`)
- `PINCER_LOG_LEVEL` (`debug|info|warn|error|fatal`)
- `PINCER_LOG_FORMAT` (`text|json`)

## Documentation

- `docs/spec.md` - end-state system design and contracts.
- `PLAN.md` - phased implementation plan and steps.
- `docs/ios-ui-plan.md` - iOS UI/UX planning details.
- `SOUL.md` - assistant phrasing guidance used by the planner when present.
- `AGENTS.md` - repository-specific agent instructions.
