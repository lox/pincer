# Pincer Implementation Plan

Status: Active
Date: 2026-02-14

This document defines phased delivery and concrete implementation steps.
The canonical end-state design is in `docs/spec.md`.

## 1. Planning assumptions

- Build smallest safe vertical slices first.
- Preserve the side-effect conveyor at every phase.
- Prefer deterministic backend controls over model discretion.
- Keep iOS as the control plane for approvals and visibility.

## 2. Phase map

1. Phase 1: Secure core conveyor (current baseline)
2. Phase 2: Real integration reads and draft flows
3. Phase 3: Scheduler + long-horizon autonomy
4. Phase 4: Memory + skills + controlled self-improvement
5. Phase 5: Production hardening and scale

## 3. Phase 1 - Secure core conveyor

Goal:

- Prove end-to-end safety path works reliably.

Steps:

1. Implement pairing and opaque bearer auth.
2. Implement chat thread/message primitives.
3. Implement proposed action persistence.
4. Implement approval endpoints and state transitions.
5. Implement action executor with idempotency.
6. Implement audit logging for all side-effect transitions.
7. Implement iOS Chat + Approvals + Settings session controls.
8. Add reproducible API and iOS E2E scripts.

Exit criteria:

- `chat -> proposed -> approved -> executed -> audited` works end to end.
- No external side effect executes without approval.
- Device revoke invalidates its token path.
- E2E scripts pass reliably.

## 4. Phase 2 - Integration reads and draft flows

Goal:

- Replace demo tool actions with real external integrations while keeping safety controls strict.

Steps:

1. Add Google OAuth token storage and encryption for user + bot identities.
2. Add Gmail read/search/snippet tools.
3. Add Gmail draft creation tools.
4. Add bot send tool behind explicit approval.
5. Keep user mailbox send disabled.
6. Add Calendar read tool.
7. Add web search/open read tools with SSRF and size constraints.
8. Add deterministic approval-card rendering for each tool type.

Exit criteria:

- Real tool reads operate via explicit `identity` selection.
- Writes/sends remain approval-gated.
- Tool args are schema-validated and auditable.

## 5. Phase 3 - Scheduler and long-horizon autonomy

Goal:

- Enable durable autonomous background execution in safe lanes.

Steps:

1. Implement job model and checkpoint persistence.
2. Implement step runner limits (time/tool/token budgets).
3. Implement scheduler triggers (`cron`, `interval`, `at`).
4. Implement wakeup dedupe and leasing.
5. Connect scheduler wakeups to job/turn execution.
6. Emit job progress to thread messages and artifacts.
7. Enforce that background jobs cannot directly execute external writes.

Exit criteria:

- Jobs can pause/resume across restarts.
- Scheduler is deterministic and deduped.
- Autonomous runs stay within internal-only constraints unless approved.

## 6. Phase 4 - Memory, skills, and controlled self-improvement

Goal:

- Add the "magical" autonomy layer without weakening policy boundaries.

Steps:

1. Formalize short-term and durable memory primitives.
2. Add timer-driven follow-up behavior using scheduler + jobs.
3. Add curated skills bound to explicit tool permissions.
4. Add internal proposal flows for skill/prompt/schedule improvements.
5. Require explicit owner approval for policy/scope/runtime-impacting changes.

Exit criteria:

- Agent can autonomously follow up, remember context, and apply skills.
- No skill or memory pathway bypasses approval/policy controls.

## 7. Phase 5 - Production hardening and scale

Goal:

- Improve reliability, operability, and organizational safety controls.

Steps:

1. Add stronger policy configuration and governance UI.
2. Add notifications and escalation reliability policies.
3. Add richer audit export and investigation workflows.
4. Add stronger secret/key management options.
5. Add multi-owner/multi-instance architecture path.
6. Add optional tamper-evident audit chain enforcement.

Exit criteria:

- Operational controls are production-ready.
- Security and audit posture supports real-world deployment requirements.

## 8. Cross-phase non-negotiables

- LLM output remains untrusted.
- External side effects always use `proposed -> approved -> executed -> audited`.
- Idempotency conflicts are hard failures with audit events.
- No policy-bypass channels.

## 9. Current checkpoint

Completed baseline:

1. Pairing + opaque bearer auth.
2. Chat + approvals + action executor + audit conveyor.
3. Device session list + revoke controls.
4. Reproducible API and iOS E2E flows.

Next priority:

1. Begin Phase 2 by integrating real Gmail/Calendar/Web read tools under current policy controls.
