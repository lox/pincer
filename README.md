<p align="left">
  <img src="assets/pincer.png" alt="Pincer logo" width="180" />
</p>

# Pincer

Pincer is a security-first autonomous assistant with a Go backend and an iOS control app.
It is designed for high autonomy with strict control over side effects.

Inspired by OpenClaw and Nanobot.

## Core idea

- The model is untrusted.
- The model may propose actions.
- Trusted code evaluates policy and executes actions.
- External side effects are never silent.

All external actions follow:

`proposed -> approved -> executed -> audited`

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
   |- Policy Engine
   |- Approval Queue
   |- Action Executor
   |- Job Runner + Scheduler
   |- Tool Registry
   |- SQLite (state + audit)
   |- Provider Client (OpenRouter/OpenAI-compatible)
```

## Local end-to-end

- `mise run backend-up`
- `mise run e2e-api`
- `mise run e2e-ios`
- `mise run backend-down`

Useful overrides:

- `PINCER_BASE_URL`
- `PINCER_DB_PATH`
- `PINCER_AUTH_TOKEN`
- `PINCER_TOKEN_HMAC_KEY`
- `PINCER_E2E_RESET_DB=0`

## Documentation

- `docs/spec.md` - end-state system design and contracts.
- `PLAN.md` - phased implementation plan and steps.
- `docs/ios-ui-plan.md` - iOS UI/UX planning details.
- `AGENTS.md` - repository-specific agent instructions.
