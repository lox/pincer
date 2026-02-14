# Pincer Phase 1 Specification (v0.2)

Status: Locked defaults for implementation
Date: 2026-02-13

## 1. Scope

Phase 1 is intentionally minimal and security-first.

- Single-owner backend instance (one runtime owner).
- iOS app is the only client/channel.
- No subprocess/sandbox execution tools.
- Calendar is read-only.
- User mailbox: read + draft only (no send).
- Bot mailbox: read + draft + send, with explicit approval for send.
- All external writes/sends are approval-gated (no auto-approval rules).

## 1.1 Delivery sequence (minimal-first)

1. Data and audit primitives
- Persist foundational entities in SQLite: `users`, `threads`, `messages`, `proposed_actions`, `idempotency`, `audit_log`.
- Enforce strict structured planner output validation before policy evaluation.

2. Action conveyor
- Implement deterministic action lifecycle: `proposed -> policy-evaluated -> approval-gated -> executed`.
- Enforce idempotency on every external write execution attempt.

3. Read-path baseline
- Enable one read integration first (Gmail read/search or web search/open).
- Validate end-to-end flow before enabling external write tools.

4. Job runner baseline
- Implement one-step job execution with checkpoint persistence and resume support.
- Emit job progress into thread messages and `job_events`.

5. Pairing and auth
- Implement device pairing and opaque bearer authentication.
- Require authentication middleware for all endpoints.
- Implement iOS shell navigation and approvals-first UX alongside pairing.

6. Approval-gated bot writes
- Add bot send flow after core primitives are stable.
- Keep user mailbox at read + draft only in Phase 1.

7. Scheduler bootstrap
- Implement `cron`, `interval`, and `at` triggers with deduplicated wakeup processing.

8. Autonomy extensions (after core acceptance criteria)
- Add durable memory patterns, timer-driven follow-up creation, and curated skill application.
- Keep autonomous execution limited to internal-only actions unless explicit approval is granted.

## 1.2 Phase 1 acceptance criteria

- No external side effect can execute without explicit approval and audit trail.
- Approval TTL is enforced with automatic expiration rejection at 24h.
- Idempotency key reuse with different args fails with conflict.
- Jobs checkpoint and resume correctly after restart/failure.
- Autonomous follow-up loops can run internally without violating external side-effect controls.

## 1.3 Explicit phase 1 deferrals

- Domain allowlists for web opens.
- Advanced prompt-injection scoring heuristics.
- Audit hash chaining.
- Multi-device policy UX beyond one-owner defaults.
- Python skill runtime isolation (future option: `pydantic/monty`).

## 1.4 Autonomy architecture (nanobot-inspired, policy-constrained)

Inspiration: [nanobot](https://github.com/lightweight-openclaw/nanobot).
Security posture: [the lethal trifecta](https://simonwillison.net/2025/Jun/16/the-lethal-trifecta/).

Memory primitive:

- Short-horizon memory: thread context + job checkpoints.
- Durable memory: internal artifacts/notes authored by trusted tool executors.
- Memory updates are internal writes and must never directly trigger external side effects.

Timer/follow-up primitive:

- Scheduler wakeups create new turns/jobs for autonomous follow-up.
- Follow-up runs may read, research, summarize, and update internal memory.
- External writes remain blocked until explicit approval.

Skill primitive:

- Skills are curated reusable instructions/workflows bound to permitted tool subsets.
- Skills execute inside normal tool policy boundaries and cannot bypass approval rules.
- No arbitrary subprocess or Python code execution in Phase 1.

Self-improvement primitive:

- Agent may propose prompt/schedule/skill changes as internal artifacts.
- Applying changes that affect policy, credentials, scopes, or external side effects is owner-gated.

Execution lanes:

1. Lane A (autonomous internal): memory writes, notes, follow-up scheduling, analysis.
2. Lane B (approval-gated external): outbound sends and external writes/exfiltration.
3. Lane C (owner-gated config): policy/scopes/credential/runtime changes.

## 2. Trust and Execution Model

- Trusted: policy engine, tool executors, SQLite store, iOS UI.
- Untrusted: LLM output, web content, email content.
- Invariant: model proposes structured actions; backend policy and executor decide/perform.

Execution pipeline:

1. Planner turn produces JSON: assistant message + proposed actions.
2. Backend validates output schema.
3. Policy evaluates each action.
4. If approval required, action enters approval queue.
5. Approved actions execute via Action Executor.
6. All steps emit audit events.

## 3. Runtime Configuration

Required environment variables:

- `PINCER_ENV`
- `PINCER_HTTP_ADDR`
- `PINCER_DATABASE_PATH`
- `PINCER_ENCRYPTION_KEY_B64` (32-byte key in base64 for token encryption)
- `PINCER_OPENROUTER_API_KEY`
- `PINCER_MODEL_PRIMARY`
- `PINCER_MODEL_FALLBACK`
- `PINCER_APPROVAL_TTL_HOURS` (default `24`)
- `PINCER_DAILY_TOKEN_BUDGET` (default `1000000`)
- `PINCER_JOB_TOKEN_BUDGET` (default `200000`)

## 4. Data Model (SQLite, WAL)

## 4.1 Core tables

`users`
- `user_id` TEXT PK
- `email` TEXT NOT NULL
- `created_at` TEXT NOT NULL

`devices`
- `device_id` TEXT PK
- `user_id` TEXT NOT NULL
- `name` TEXT
- `public_key` TEXT
- `revoked_at` TEXT
- `created_at` TEXT NOT NULL

`auth_tokens`
- `token_id` TEXT PK
- `device_id` TEXT NOT NULL
- `token_hash` TEXT NOT NULL UNIQUE
- `expires_at` TEXT NOT NULL
- `last_used_at` TEXT
- `created_at` TEXT NOT NULL

`oauth_tokens`
- `token_id` TEXT PK
- `user_id` TEXT NOT NULL
- `provider` TEXT NOT NULL (`google`)
- `identity_type` TEXT NOT NULL (`user` | `bot`)
- `encrypted_refresh_token` BLOB NOT NULL
- `scopes` TEXT NOT NULL
- `created_at` TEXT NOT NULL
- `updated_at` TEXT NOT NULL
- UNIQUE(`user_id`, `provider`, `identity_type`)

`threads`
- `thread_id` TEXT PK
- `user_id` TEXT NOT NULL
- `channel` TEXT NOT NULL (`ios`)
- `created_at` TEXT NOT NULL

`messages`
- `message_id` TEXT PK
- `thread_id` TEXT NOT NULL
- `role` TEXT NOT NULL (`user` | `assistant` | `system`)
- `content` TEXT NOT NULL (max 64KB UTF-8 bytes)
- `created_at` TEXT NOT NULL

`jobs`
- `job_id` TEXT PK
- `thread_id` TEXT NOT NULL
- `user_id` TEXT NOT NULL
- `goal` TEXT NOT NULL
- `state` TEXT NOT NULL (`PENDING` | `RUNNING` | `WAITING_APPROVAL` | `COMPLETED` | `FAILED` | `CANCELLED` | `PAUSED_BUDGET`)
- `checkpoint_blob` BLOB (max 512KB)
- `next_wakeup_at` TEXT
- `budget_json` TEXT NOT NULL
- `created_at` TEXT NOT NULL
- `updated_at` TEXT NOT NULL

`job_events`
- `event_id` TEXT PK
- `job_id` TEXT NOT NULL
- `type` TEXT NOT NULL
- `payload_json` TEXT NOT NULL
- `created_at` TEXT NOT NULL

`schedules`
- `schedule_id` TEXT PK
- `user_id` TEXT NOT NULL
- `trigger_type` TEXT NOT NULL (`cron` | `interval` | `at`)
- `trigger_config_json` TEXT NOT NULL
- `payload_json` TEXT NOT NULL
- `timezone` TEXT NOT NULL (IANA TZ, e.g. `Australia/Melbourne`)
- `enabled` INTEGER NOT NULL
- `created_at` TEXT NOT NULL

`wakeup_events`
- `event_id` TEXT PK
- `schedule_id` TEXT NOT NULL
- `scheduled_for` TEXT NOT NULL (UTC RFC3339)
- `lease_until` TEXT
- `status` TEXT NOT NULL (`PENDING` | `LEASED` | `COMPLETED`)
- `dedupe_key` TEXT NOT NULL UNIQUE

`proposed_actions`
- `action_id` TEXT PK
- `user_id` TEXT NOT NULL
- `source` TEXT NOT NULL (`chat` | `job` | `schedule`)
- `source_id` TEXT NOT NULL
- `tool` TEXT NOT NULL
- `args_json` TEXT NOT NULL
- `risk_class` TEXT NOT NULL
- `justification` TEXT
- `idempotency_key` TEXT NOT NULL
- `status` TEXT NOT NULL (`PENDING` | `APPROVED` | `REJECTED` | `EXECUTED`)
- `rejection_reason` TEXT
- `expires_at` TEXT NOT NULL
- `created_at` TEXT NOT NULL
- UNIQUE(`user_id`, `tool`, `idempotency_key`)

`idempotency`
- `owner_id` TEXT NOT NULL
- `tool_name` TEXT NOT NULL
- `key` TEXT NOT NULL
- `args_hash` TEXT NOT NULL
- `result_hash` TEXT NOT NULL
- `created_at` TEXT NOT NULL
- PRIMARY KEY(`owner_id`, `tool_name`, `key`)

`artifacts`
- `artifact_id` TEXT PK
- `job_id` TEXT NOT NULL
- `type` TEXT NOT NULL
- `blob` BLOB NOT NULL (max 2MB)
- `created_at` TEXT NOT NULL

`audit_log`
- `entry_id` TEXT PK
- `event_type` TEXT NOT NULL
- `entity_id` TEXT NOT NULL
- `payload_json` TEXT NOT NULL
- `created_at` TEXT NOT NULL

## 4.2 Retention and pruning

Run daily prune job:

- `idempotency`: 90 days
- `audit_log`: 90 days
- `messages`: 30 days
- `artifacts`: 90 days
- expired `auth_tokens`: immediate delete

## 5. Tooling and Risk Classification

Tool call envelope must include explicit identity:

```json
{
  "tool": "gmail_search_threads",
  "identity": "user",
  "args": {}
}
```

`identity` is required on all Google tools (`user` or `bot`).

Phase 1 tool registry:

- `gmail_search_threads` (`READ`)
- `gmail_get_snippet` (`READ`)
- `gmail_get_full` (`READ`, approval required)
- `gmail_create_draft_reply` (`WRITE`, approval required)
- `gmail_send_draft` (`EXFILTRATION`, bot identity only, approval required)
- `gcal_list_events` (`READ`)
- `web_search` (`READ`)
- `web_open` (`READ`, SSRF constrained)
- `artifact_put` (`WRITE`, internal destination)
- `notes_write` (`WRITE`, internal destination)

Internal writes (`artifact_put`, `notes_write`) do not require human approval.
These internal writes are also the base memory primitive for autonomy in Phase 1.

## 6. Policy Engine Rules (Phase 1)

Deterministic rule set:

1. Any external `WRITE`/`EXFILTRATION` action requires explicit approval.
2. Background job steps must not execute external `WRITE`/`EXFILTRATION` tools directly.
3. Background jobs may create proposed actions; Action Executor executes only after approval.
4. Pending approvals expire after 24h and auto-reject with reason `expired`.
5. No allowlist auto-approval behavior in Phase 1.
6. Per-turn untrusted-ingest guard: if a turn ingests untrusted email/web content, block external `WRITE`/`EXFILTRATION` proposals in that same turn (`POLICY_BLOCKED_UNTRUSTED_TURN`).
7. `web_open` constraints: HTTPS-only by default; no IP literal hostnames; block localhost/RFC1918/link-local/loopback/private ranges; max 5 redirects; max 2MB fetch body; max 50KB extracted text passed to model.
8. No HTTP POST/PUT tools in Phase 1.

## 7. Approval Lifecycle

States:

- `PENDING`
- `APPROVED`
- `REJECTED`
- `EXECUTED`

Flow:

1. Action proposed and validated.
2. Policy marks approval required.
3. Row inserted with `status=PENDING`, `expires_at=now+24h`.
4. iOS user approves or rejects (FaceID on device).
5. Action Executor leases approved actions and executes exactly once using idempotency.
6. On success: `EXECUTED`.
7. On error: action remains `APPROVED` with retry metadata (bounded retries), audit logged.
8. Expiry worker marks stale pending actions `REJECTED` with reason `expired`.

## 8. Idempotency

External write execution requires idempotency record:

- Unique key scope: (`owner_id`, `tool_name`, `idempotency_key`).
- Store `args_hash` on first execution.
- If key reused with different `args_hash`, return HTTP 409 and emit audit event `idempotency_conflict`.
- Idempotency retention: 90 days.

## 9. Job System

Step limits:

- Max wall time per step: 60s
- Max tool calls per step: 10
- Max output tokens per step: configurable (default 8k)
- Max job tokens: 200k
- Daily backend token budget: 1M

Budget behavior:

- Step overrun: abort step, persist checkpoint, retry at next wakeup.
- Job token budget exceeded: fail job with budget reason.
- Daily budget exceeded: set runnable jobs to `PAUSED_BUDGET` until budget window resets.
- Jobs may autonomously write memory/notes and schedule follow-up work within policy constraints.

Job-thread relation:

- `jobs.thread_id` required.
- Job status updates and artifacts are posted into thread as `system` messages.

## 10. Scheduler

Supported triggers:

- `cron`
- `interval`
- `at`

Timezone:

- Schedule stored with IANA timezone.
- Next fire computed in schedule timezone.
- Persist scheduled fire times in UTC.
- DST rules:
- Ambiguous local time: first occurrence.
- Nonexistent local time: roll forward to next valid local time.

Dedupe key:

- `sha256(schedule_id + "|" + scheduled_for_utc + "|" + payload_hash)`
- `payload_hash = sha256(canonical_json(payload_json))`

## 11. Model Provider Contract

Provider: OpenRouter using OpenAI-compatible chat completions.

Requirements:

- Tool calling enabled.
- Streaming supported for assistant text.
- Retry on transient upstream errors (`429`, `502`, timeout).
- Fallback model chain: primary then one fallback.

Required model output shape:

```json
{
  "assistant_message": "string",
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

Invalid output handling:

1. One repair retry on same model.
2. If still invalid, one attempt on fallback model.
3. If still invalid, fail turn/job with `FAILED_MODEL_OUTPUT`.

## 12. Google OAuth Scope Matrix

User identity:

- Gmail read/draft: `gmail.readonly`, `gmail.compose`
- Calendar read: `calendar.readonly`

Bot identity:

- Gmail read/send/draft: `gmail.readonly`, `gmail.send`, `gmail.compose`

No Calendar write scopes in Phase 1.

## 13. iOS Pairing and Auth

Pairing:

1. Backend issues short-lived pairing code.
2. App submits code + generated device public key.
3. Backend binds device and returns opaque bearer token.
4. Token stored in iOS Keychain.

Auth:

- Bearer token over TLS only.
- Token TTL 30 days with sliding renewal.
- Revocation by `device_id`.
- One active (non-revoked) device by default in Phase 1.

## 13.1 iOS UI/UX planning baseline

Notification contract:

- Notification types: `approval_needed`, `approval_expiring`, `job_failed`, `job_completed`, `proactive_reach_out`.
- `proactive_reach_out` is policy-gated and must map to a concrete thread/job.
- `proactive_reach_out.reason` values: `operator_attention_needed`, `clarification_needed`, `important_update`, `follow_up_available`.
- Push payloads must contain opaque ids only; sensitive details are fetched after authenticated app open.
- Notification delivery is rate-limited per entity to prevent spam loops.

Detailed planning reference: `docs/ios-ui-plan.md`.

Phase 1 UX contract:

- The iOS app is a control surface, not an autonomous decision-maker.
- Approvals are first-class UI entities with clear lifecycle and expiry visibility.
- External side effects are never represented as completed until execution is confirmed.
- Chat, Approvals, Work, Schedules, and Settings are required primary surfaces.

## 14. REST API (Phase 1)

`POST /v1/pairing/code`
- Create short-lived pairing code.

`POST /v1/pairing/bind`
- Input: pairing code, device metadata, public key.
- Output: bearer token + expiry.

`POST /v1/chat/threads`
- Create thread.

`POST /v1/chat/threads/{thread_id}/messages`
- Append user message, trigger planner turn.

`GET /v1/chat/threads/{thread_id}/messages`
- List timeline.

`GET /v1/approvals?status=pending`
- List pending approvals.

`GET /v1/approvals/{action_id}`
- Approval detail.

`POST /v1/approvals/{action_id}/approve`
- Approve action.

`POST /v1/approvals/{action_id}/reject`
- Reject action with reason.

`GET /v1/jobs`
- List jobs by state.

`POST /v1/jobs`
- Create job bound to thread.

`POST /v1/jobs/{job_id}/cancel`
- Cancel job.

`GET /v1/jobs/{job_id}`
- Job detail + events + artifacts metadata.

`GET /v1/schedules`
- List schedules.

`POST /v1/schedules`
- Create schedule.

`PATCH /v1/schedules/{schedule_id}`
- Enable/disable or edit trigger.

`POST /v1/schedules/{schedule_id}/run-now`
- Enqueue immediate wakeup.

`GET /v1/settings/policy`
- Return effective policy flags/limits.

`GET /v1/audit`
- List audit entries (paginated).

`GET /v1/notifications`
- List notification events for the device (including proactive reach-out events with reason code).

## 15. Approval Card Rendering Contract

Approval text is backend-rendered deterministically (not LLM-generated).

Required fields in approval payload:

- `tool_name`
- `human_summary`
- `target_entity`
- `risk_class`
- `preview_or_diff`
- `source_type` (`chat` | `job` | `schedule`)
- `expires_at`

## 16. Security Controls Checklist (Phase 1)

- Token encryption at rest using env-provided key.
- TLS required.
- Strict schema validation for tool args.
- Explicit identity required on Google tool calls.
- Untrusted content labeling on ingest.
- Side effects only through propose->approve->execute flow.
- Idempotency enforced for external writes.
- SSRF protections on `web_open`.
- Remote image loading stripped from email content presented to model.
- Attachments never passed raw to model.

## 17. Non-Goals (Phase 1)

- Multi-tenant runtime.
- WhatsApp channel.
- Calendar write/apply.
- Domain-wide delegation.
- Shell/subprocess tools.
- Automated recipient/domain allowlists.
- Compliance export tooling.
