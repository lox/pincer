# 🧠 Pincer (Backend + iOS Control App)

A security-first autonomous assistant that can:

* Read and draft Gmail
* Read Google Calendar
* Perform long-horizon web research
* Run scheduled jobs
* Propose external actions (never execute silently)
* Be controlled via an iOS app

Built as a single Go backend with a minimal, approval-centric iOS control surface.

---

# ✨ Design Goals

This project is intentionally **not** a chat toy.

It is:

* A **durable autonomous system**
* With **strict side-effect controls**
* Designed to avoid prompt injection blast radius
* Capable of **long-running background research**
* Simple enough to reason about in production

---

# 🏗 Architecture Overview

```
iOS App
   │
   ▼
Go Backend (single binary)
   ├── OpenRouter (LLM)
   ├── Google Workspace (OAuth)
   ├── Job Runner (long-horizon work)
   ├── Scheduler (timers)
   ├── Policy Engine (side-effect gating)
   ├── Approval Queue
   └── SQLite (state + audit)
```

---

# 🧪 Local E2E (tmux)

Use these commands to run the current MVP slice (`chat -> pending approval -> approve -> executed/audited`) locally:

* `mise run backend-up` - start backend in a dedicated `tmux` session (`pincer-backend`)
* `mise run e2e-api` - run the API end-to-end verification flow
* `mise run e2e-ios` - run simulator UI + backend end-to-end verification
* `mise run backend-down` - stop the `tmux` backend session

Defaults:

* backend address: `:8080`
* base URL: `http://127.0.0.1:8080`
* DB path: `/tmp/pincer-e2e.db`
* dev token: `dev-token`

Override via env vars:

* `PINCER_TMUX_SESSION`
* `PINCER_HTTP_ADDR`
* `PINCER_BASE_URL`
* `PINCER_DB_PATH`
* `PINCER_DEV_TOKEN`
* `PINCER_E2E_RESET_DB=0` (to keep DB between runs)

---

# 🔐 Core Security Model

**The model is untrusted.**

It may:

* Propose actions
* Plan steps
* Summarize content

It may NOT:

* Send emails directly
* Modify calendar
* Message external systems
* Execute side effects

We align to Simon Willison's [the lethal trifecta](https://simonwillison.net/2025/Jun/16/the-lethal-trifecta/) through three controls:

1. Planner vs execution separation.
2. Structured action proposals with trusted schema validation.
3. Human approval before any external side effect.

All external actions follow:

```
Propose → Validate → Approve → Execute → Audit
```

No silent writes. Ever.

---

# 👤 Identities

The system uses two Google Workspace identities:

### `bot@yourdomain`

* Read + draft + send
* All sends require approval

### `you@yourdomain`

* Read + draft only
* No send in Phase 1

No domain-wide delegation. OAuth per mailbox.

---

# 📱 iOS App

The iOS app is the **control plane**, not the brain.
Detailed UX planning lives in `docs/ios-ui-plan.md`.

## Screens

* Chat
* Approvals
* Schedules
* Jobs
* Settings

Approvals require FaceID / TouchID.
The bot may proactively notify you for important updates, clarifications, or intervention needs.

All approval cards are rendered by the backend — not the model.

---

# 🧩 Phase 1 Scope

## Included

* Gmail read + draft (user)
* Gmail read + draft + send (bot, approval-gated)
* Calendar read-only
* Web search + fetch (SSRF-protected)
* Background research jobs
* Scheduled jobs
* Approval system
* SQLite persistence
* OpenRouter (OpenAI-compatible API)
* Single-owner backend instance

## Not Included

* WhatsApp
* Calendar writes
* Multi-user backend
* Domain-wide delegation
* Arbitrary shell execution
* Heavy sandboxing (Fence/microVM)

## Delivery Sequence (Build Order)

1. Core data and audit primitives
- Persist `users`, `threads`, `messages`, `proposed_actions`, `idempotency`, and `audit_log`.
- Enforce strict planner output schema validation before policy evaluation.

2. Action conveyor
- Implement `propose -> policy -> approval -> execute` flow.
- Enforce idempotency for all external write execution.

3. Read path baseline
- Enable one read integration first (Gmail read/search or web search/open).
- Validate end-to-end flow before enabling external writes.

4. Jobs baseline
- Add one-step job runner with checkpoint persistence and resume behavior.
- Emit job progress into thread/system events.

5. Pairing and auth
- Implement device pairing and opaque bearer auth.
- Require auth middleware on all API endpoints.
- Build iOS shell and approvals UX first so safety controls are visible from day one.

6. Approval-gated writes
- Add bot send flow last.
- Keep user mailbox limited to read + draft in Phase 1.

7. Scheduler bootstrap
- Implement `cron`, `interval`, and `at` scheduling with deduped wakeups.

8. Autonomy extensions (after core criteria)
- Add durable memory flows, timer-driven follow-ups, and curated skills.
- Keep autonomous execution in internal-only lanes unless explicitly approved.

## Phase 1 Acceptance Criteria

- Every external side effect is `proposed -> approved -> executed` and audited.
- Approval TTL is enforced and stale requests auto-reject.
- Idempotency key collisions with mismatched args fail hard.
- Jobs checkpoint and resume after process restart.
- Autonomous jobs can create follow-up work and update memory without unsafe side effects.

## Explicit Deferrals

- Domain allowlists.
- Multi-device policy UX beyond one-owner defaults.
- Audit hash chaining.
- Advanced prompt-injection heuristics.
- Python skill runtime isolation (for example, [pydantic/monty](https://github.com/pydantic/monty)).

## Autonomy Within Safe Constraints

Inspired by [nanobot](https://github.com/lightweight-openclaw/nanobot), the autonomy layer sits on top of the security primitives above.

Memory:

- Short-term memory lives in checkpoints and thread context.
- Durable memory is written to internal notes/artifacts.
- Memory writes are internal and do not bypass policy for external actions.

Timer-driven follow-up:

- Jobs can schedule follow-up work via timers.
- Follow-up turns can read, research, summarize, and update internal memory autonomously.
- Any external write/send remains approval-gated.

Skills:

- Skills are curated reusable workflows/prompts bound to allowed tools.
- Skills can improve quality/speed, but cannot bypass tool policy or approval rules.

Self-improvement:

- The agent may propose prompt, schedule, and skill updates as internal artifacts.
- Applying changes that impact policy, scopes, or external side effects requires explicit owner approval.

Safety lanes:

1. Lane A: autonomous internal actions (memory, notes, follow-up scheduling, analysis).
2. Lane B: approval-gated external actions (send/write/exfiltration).
3. Lane C: owner-gated configuration changes (policy, scopes, credentials, runtime changes).

---

# 🔄 Long-Horizon Research

The agent supports background jobs that:

* Perform web research
* Read email/calendar
* Write internal notes/artifacts
* Update memory context for future turns
* Schedule timer-driven follow-up work
* Apply curated skills
* Propose actions

They cannot execute external writes.

Jobs are:

* Checkpointed
* Budget-limited
* Resumable
* Inspectable in the app

---

# ⏱ Scheduler

Supports:

* Cron schedules
* Interval schedules
* One-shot triggers

Wakeups create durable events and feed into the job runner.
This also powers autonomous follow-up loops.

All times stored in UTC.
Schedules evaluated in user timezone.

---

# 🧪 Prompt Injection Controls

Mandatory protections:

* All web/email content marked as UNTRUSTED
* Strict tool argument validation
* No external WRITE/EXFILTRATION tools after untrusted ingestion in same turn
* No HTTP POST/PUT tools
* No IP literal / RFC1918 web access
* Two-phase write model

Defense-in-depth, not vibes.

---

# 📦 Data Storage

SQLite (WAL mode) stores:

* Threads & messages
* Jobs & checkpoints
* Schedules & wakeups
* Proposed actions
* Idempotency keys
* OAuth tokens (encrypted)
* Audit log

---

# 🔁 Idempotency

All external writes require an `idempotency_key`.

Reusing a key with different arguments results in a hard conflict error.

Keys retained for 90 days.

---

# ⚙ Configuration (Phase 1 Defaults)

* Single owner per backend instance
* Approval TTL: 24h
* Max message size: 64KB
* Max artifact size: 2MB
* Web text passed to model: 50KB cap
* Job step timeout: 60s
* Per-job token budget: 200k
* Daily token budget: 1M

---

# 🚀 Getting Started (Conceptual)

1. Create Google Workspace bot account
2. Configure OAuth credentials
3. Configure OpenRouter API key
4. Set master encryption key
5. Run backend
6. Pair iOS app
7. Approve Gmail + Calendar access
8. Start chatting

---

# 🛣 Future Directions

* WhatsApp adapter
* Calendar writes (approval-gated)
* Multi-user backend
* Allowlist policies
* Stronger sandboxing for tool subprocesses
* Audit hash chaining
* Vector memory
* Python skill runtime isolation via `pydantic/monty`

---

# 🧭 Philosophy

This system is built around a simple principle:

> Autonomy without authority.

The agent can think, research, and plan.

Only you can let it act.

---
