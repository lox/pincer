# Pincer System Specification

Status: Canonical design contract
Date: 2026-02-14

This document defines the target system design for Pincer.
Implementation sequencing and phase gates are tracked in `PLAN.md`.

## 1. Purpose

Pincer is a security-first autonomous assistant that can:

- operate over long horizons (research, planning, follow-up),
- integrate with external systems (mail, calendar, web),
- and remain safe by requiring explicit approval for risky side effects.

## 2. Core invariants

1. LLM output is untrusted.
2. External side effects must flow through trusted code paths.
3. No external write/send executes without explicit policy decision.
4. Idempotency gates all external write execution.
5. Every side-effect transition is auditable.

Canonical side-effect conveyor:

`proposed -> approved -> executed -> audited`

## 3. Trust model

Trusted:

- policy engine
- tool executors
- SQLite persistence
- iOS control UI

Untrusted:

- planner/model output
- email/web content

Operating principle:

- model may propose,
- trusted code decides and executes.

## 4. Architecture

Primary components:

- HTTP API server (Go)
- Trigger ingestion layer (chat message, job, schedule, heartbeat, delegate callbacks)
- Ingress work queue and durable outbox
- SQLite (WAL)
- Conversation/session store with checkpoints
- Turn orchestrator (planner -> tool loop)
- Tool registry and validators
- Policy engine
- Approval queue
- Action executor
- Job runner
- Scheduler
- Provider client (OpenAI-compatible)

Reference flow (governed agentic turn):

1. A trigger enqueues a work item (chat input, job wakeup, heartbeat event, or subagent callback).
2. Turn orchestrator loads thread/session context and budget (window limits, tool-step limits).
3. Planner returns either final text or tool calls using trusted tool schemas.
4. For each tool call:
   - tool arguments are validated and classified;
   - internal/tool-safe calls execute and produce typed tool result messages;
   - external-impacting calls become `proposed_actions` for policy.
5. Tool results are appended to context and re-entered into the same turn loop.
6. Loop repeats until no tool calls remain, a final assistant response is produced, or bounded limits are hit (`max_tool_steps`, `max_tool_tokens`, `max_context_messages`).
7. Proposal flow remains unchanged: `proposed -> approved -> executed -> audited`.
8. All step transitions and invalid model output events are written to audit.

## 5. Identity and authentication

Supported identities:

- user identity (`identity: "user"`)
- bot identity (`identity: "bot"`)

Identity must be explicit on every integration tool call.

Auth model:

- device pairing via short-lived pairing code,
- opaque bearer tokens (`pnr_<token_id>.<secret>`),
- hashed token storage (HMAC-SHA256),
- token TTL + sliding renewal,
- device-scoped revocation.

## 6. Data model

SQLite is the system of record.

Core tables:

- `users`
- `devices`
- `auth_tokens`
- `oauth_tokens`
- `threads`
- `messages`
- `jobs`
- `job_events`
- `schedules`
- `wakeup_events`
- `proposed_actions`
- `idempotency`
- `artifacts`
- `audit_log`

### 6.1 Required constraints

- `proposed_actions` uniqueness on `(user_id, tool, idempotency_key)`
- `idempotency` primary key on `(owner_id, tool_name, key)`
- bounded payload sizes for message/checkpoint/artifact blobs
- durable timestamps in RFC3339/UTC for event ordering

### 6.2 Retention defaults

- idempotency: 90 days
- audit: 90 days
- artifacts: 90 days
- messages: 30 days

## 7. Tool system

Tool interface requirements:

- deterministic name and risk class,
- strict argument schema validation,
- explicit execution entrypoint.

Risk classes:

- `READ`
- `WRITE`
- `EXFILTRATION`
- `DESTRUCTIVE`
- `HIGH`

Baseline tool families:

- Gmail (user and bot identities)
- Calendar
- Web (`search`, `open`)
- Internal memory/artifact tools

## 8. Policy engine

Policy is deterministic and code-enforced.

Mandatory rules:

1. External `WRITE` and `EXFILTRATION` actions require explicit approval.
2. Background jobs cannot directly execute external writes/sends.
3. Jobs may create proposed actions for later approval.
4. Approval requests expire and auto-reject.
5. Untrusted-ingest turns cannot directly trigger external write/send in the same turn.
6. Web access enforces SSRF protections (no local/private targets, capped redirects/bytes).

## 9. Approval lifecycle

Action states:

- `PENDING`
- `APPROVED`
- `REJECTED`
- `EXECUTED`

Lifecycle:

1. proposal persisted with risk metadata,
2. policy decision computed,
3. approval required -> queue entry,
4. user approval/rejection from iOS,
5. executor executes approved action with idempotency,
6. audit records all transitions.

## 10. Idempotency contract

For external side effects:

- require idempotency key,
- store argument hash and result hash,
- key reuse with mismatched args is a hard conflict,
- conflict emits audit event (`idempotency_conflict`).

## 10.1 Turn safety controls

Turn execution is bounded and replay-safe:

- `max_tool_steps` and `max_tool_tokens` are enforced per work item.
- every work item has a deterministic turn identifier.
- invalid/unstable model output follows repair then `FAILED_MODEL_OUTPUT`.
- tool-call loops cannot bypass proposal/policy for external side effects.

## 11. Jobs, scheduler, and autonomy primitives

### 11.0 Turn execution kernel

Each user request or trigger is a deterministic turn:

- Planner and executor are separated.
- Planner output may include tool calls.
- Tool calls execute in a bounded loop with context updates between rounds.
- Final turn artifact includes:
  - assistant final message,
  - appended tool-result messages,
  - normalized proposed actions.
- Turn outcomes are persisted as part of thread state and can be resumed after process restart.

### 11.1 Jobs

Jobs run in bounded steps with:

- wall clock limits,
- tool-call limits,
- token budgets,
- checkpoint persistence.

### 11.2 Scheduler

Scheduler supports:

- `cron`
- `interval`
- `at`

Wakeups are deduplicated and durable.
Timezone handling uses IANA zone definitions; execution times are persisted in UTC.

### 11.3 Memory

Memory model:

- short-term: thread context + checkpoints,
- durable: internal notes/artifacts.

Memory writes are internal actions and do not bypass approval for external side effects.

### 11.4 Skills and self-improvement

Skills are curated workflows constrained by policy and allowed toolsets.
Self-improvement proposals are internal artifacts until owner-approved when they affect policy/scopes/runtime behavior.

### 11.5 Proactive triggers and delegated work

- Background scheduler wakeups and heartbeat-driven events are first-class turn triggers.
- Turn orchestrators may spawn delegated internal work units (subagents) with explicit capability and scope limits.
- Delegated work emits callback events that re-enter the same turn/event stream.
- Delegated outputs are internal messages until policy classifies and proposes side effects.

## 12. Model provider contract

Provider interface must support:

- OpenAI-compatible chat API,
- tool calling,
- retries and timeout controls,
- fallback model chain.

Planner output contract:

```json
{
  "assistant_message": "string",
  "tool_calls": [
    {
      "id": "string",
      "name": "string",
      "arguments": {}
    }
  ],
  "proposed_actions": [
    {
      "tool": "string",
      "identity": "user|bot|null",
      "args": {},
      "justification": "string"
    }
  ]
}
```

`tool_calls` is optional; when present, loop execution must continue until terminal state.

Invalid output handling:

1. one repair retry,
2. one fallback model attempt,
3. fail turn/job with `FAILED_MODEL_OUTPUT`.

## 13. API surface

Core API groups:

- pairing/auth:
  - `POST /v1/pairing/code`
  - `POST /v1/pairing/bind`
  - `GET /v1/devices`
  - `POST /v1/devices/{device_id}/revoke`
- chat:
  - `POST /v1/chat/threads`
  - `POST /v1/chat/threads/{thread_id}/messages`
  - `GET /v1/chat/threads/{thread_id}/messages`
- approvals:
  - `GET /v1/approvals`
  - `POST /v1/approvals/{action_id}/approve`
  - `POST /v1/approvals/{action_id}/reject`
- jobs:
  - `GET /v1/jobs`
  - `POST /v1/jobs`
  - `GET /v1/jobs/{job_id}`
  - `POST /v1/jobs/{job_id}/cancel`
- schedules:
  - `GET /v1/schedules`
  - `POST /v1/schedules`
  - `PATCH /v1/schedules/{schedule_id}`
  - `POST /v1/schedules/{schedule_id}/run-now`
- system:
  - `GET /v1/settings/policy`
  - `GET /v1/audit`
  - `GET /v1/notifications`

## 14. iOS control-plane contract

The iOS app is a control surface, not an autonomous decision-maker.

Required surfaces:

- Chat
- Approvals
- Schedules
- Jobs
- Settings

Approval UX requirements:

- deterministic backend-rendered approval summaries,
- clear risk and target display,
- explicit approve/reject actions,
- biometric confirmation where enabled.

Notifications include intervention and proactive reach-out events with rate limits.

## 15. Security controls checklist

- strict schema validation before policy evaluation
- untrusted content labeling
- no direct model-to-side-effect path
- secret redaction before model input
- token and refresh-secret protection at rest
- TLS-only transport
- side-effect idempotency enforcement
- audit logging for proposal/approval/execution/rejection/conflict
- SSRF protections for web fetch tools
- bounded turn loop with persisted checkpoints and replay-safe IDs

## 16. Deliberate exclusions (unless explicitly planned)

- arbitrary subprocess/shell tools
- policy bypass pathways
- silent recipient/domain allowlist execution
- hidden side-effect channels

Implementation priorities and rollout sequencing live in `PLAN.md`.
