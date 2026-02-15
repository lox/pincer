# Pincer Implementation Plan

Status: Active
Last updated: 2026-02-15

This document tracks phased delivery and concrete implementation status.
The canonical end-state design is in `docs/spec.md`.
The canonical control-plane wire contract is in `docs/protocol.md`.

## 1. Planning assumptions

- [x] Build smallest safe vertical slices first.
- [x] Preserve the side-effect conveyor at every phase.
- [x] Prefer deterministic backend controls over model discretion.
- [x] Keep iOS as the control plane for approvals and visibility.

## 2. Phase status

- [x] Phase 1: Secure core conveyor
- [ ] Phase 2: Integration reads and draft flows
- [ ] Phase 3: Scheduler and long-horizon autonomy
- [ ] Phase 4: Memory, skills, and controlled self-improvement
- [ ] Phase 5: Production hardening and scale

## 3. Phase 1 - Secure core conveyor

Goal:

- [x] Prove end-to-end safety path works reliably.

Steps:

- [x] Implement pairing and opaque bearer auth.
- [x] Migrate control-plane transport to ConnectRPC/protobuf handlers.
- [x] Implement chat thread/message primitives.
- [x] Implement proposed action persistence.
- [x] Implement approval endpoints and state transitions.
- [x] Implement action executor with idempotency.
- [x] Add explicit-approval `run_bash` execution with bounded timeout/output capture and audited results.
- [x] Stream turn/thread events for tool execution and command output deltas (`TurnsService.StartTurn`, `EventsService.WatchThread`).
- [x] Implement audit logging for all side-effect transitions.
- [x] Implement iOS Chat + Approvals + Settings session controls.
- [x] Unify approval state between Approvals tab and inline Chat indicators.
- [x] Render approval and execution outcomes directly in the Chat timeline.
- [x] Generate and wire iOS Connect Swift stubs from protobuf (`buf` + `connect-swift`).
- [x] Add reproducible API and iOS E2E scripts.

Exit criteria:

- [x] `chat -> proposed -> approved -> executed -> audited` works end to end.
- [x] No external side effect executes without approval.
- [x] Device revoke invalidates its token path.
- [x] App-facing control-plane flows run through ConnectRPC services.
- [x] E2E scripts pass reliably.

## 4. Phase 2 - Integration reads and draft flows

Goal:

- [ ] Replace demo tool actions with real external integrations while keeping safety controls strict.

Steps:

- [ ] Add Google OAuth token storage and encryption for user + bot identities.
- [ ] Add Gmail read/search/snippet tools.
- [ ] Add Gmail draft creation tools.
- [ ] Add bot send tool behind explicit approval.
- [ ] Keep user mailbox send disabled.
- [ ] Add Calendar read tool.
- [x] Add web search tool (Kagi Search API) and web summarize tool (Kagi Universal Summarizer API).
- [x] Add web_fetch raw URL read tool with SSRF and size constraints.
- [ ] Add `image_describe` multimodal tool for image analysis via vision-capable model.
- [ ] Add safe image rendering in iOS chat with provenance tracking and proxy (Camo-style) to prevent exfiltration.
- [ ] Add structured inline citations for web content summaries (source markers in text + sources array in planner output, rendered as tappable chips in iOS with domain/title/link).
- [ ] Add deterministic approval-card rendering for each tool type.

Exit criteria:

- [ ] Real tool reads operate via explicit `identity` selection.
- [ ] Writes/sends remain approval-gated.
- [ ] Tool args are schema-validated and auditable.

## 5. Phase 3 - Scheduler and long-horizon autonomy

Goal:

- [ ] Enable durable autonomous background execution with a governed turn execution kernel.

Steps:

- [ ] Define work item ingestion from user messages, jobs, schedules, and heartbeat events.
- [ ] Implement turn orchestration with bounded planner-tool loop (`max_tool_steps`, `max_tool_tokens`, `max_context_messages`).
- [ ] Persist turn checkpoints after each tool step so turns can resume across restarts.
- [ ] Implement deterministic repair/fallback handling for malformed tool-call/model outputs.
- [ ] Implement step runner limits (time/tool/token budgets) with clear failure states.
- [ ] Implement scheduler triggers (`cron`, `interval`, `at`).
- [ ] Implement wakeup dedupe and leasing.
- [ ] Connect scheduler wakeups to job/turn execution.
- [ ] Emit job progress to thread messages and artifacts.
- [ ] Enforce that background jobs cannot directly execute external writes.
- [ ] Add internal event/bus abstraction for subagent callback delivery.

Exit criteria:

- [ ] Jobs can pause/resume across restarts.
- [ ] Scheduler is deterministic and deduped.
- [ ] Autonomous runs stay within internal-only constraints unless approved.

## 6. Phase 4 - Memory, skills, and controlled self-improvement

Goal:

- [ ] Add the "magical" autonomy layer without weakening policy boundaries.

Steps:

- [ ] Formalize short-term and durable memory primitives.
- [ ] Add timer-driven follow-up behavior using scheduler + jobs.
- [ ] Add delegated work unit/subagent support with strict capability and scope policies.
- [ ] Add curated skills bound to explicit tool permissions.
- [ ] Add internal proposal flows for skill/prompt/schedule improvements.
- [ ] Require explicit owner approval for policy/scope/runtime-impacting changes.

Exit criteria:

- [ ] Agent can autonomously follow up, remember context, and apply skills.
- [ ] No skill or memory pathway bypasses approval/policy controls.

## 7. Phase 5 - Production hardening and scale

Goal:

- [ ] Improve reliability, operability, and organizational safety controls.

Steps:

- [ ] Add stronger policy configuration and governance UI.
- [ ] Add notifications and escalation reliability policies.
- [ ] Add richer audit export and investigation workflows.
- [ ] Add stronger secret/key management options.
- [ ] Add multi-owner/multi-instance architecture path.
- [ ] Add optional tamper-evident audit chain enforcement.

Exit criteria:

- [ ] Operational controls are production-ready.
- [ ] Security and audit posture supports real-world deployment requirements.

## 8. Cross-phase non-negotiables

- [x] LLM output remains untrusted.
- [x] External side effects always use `proposed -> approved -> executed -> audited`.
- [x] Idempotency conflicts are hard failures with audit events.
- [x] No policy-bypass channels.
- [ ] All planner-tool turns are bounded, replay-safe, and audit-covered.
- [ ] Triggered turns (jobs/schedules/heartbeat/subagents) must use the same proposal pipeline.

## 9. Current checkpoint

- [x] Pairing + opaque bearer auth.
- [x] ConnectRPC/protobuf control-plane handlers registered for auth/devices/threads/turns/events/approvals/jobs/schedules/system.
- [x] Chat + approvals + action executor + audit conveyor.
- [x] SOUL-guided planner prompt loading from `SOUL.md`.
- [x] `run_bash` tool path with approval gating and auditable execution output in chat.
- [x] Turn/thread event streaming with incremental command output events.
- [x] Inline chat approval/execution timeline with shared approval state from Approvals tab.
- [x] Basic native markdown rendering in iOS chat messages using `AttributedString` (`inlineOnlyPreservingWhitespace`).
- [x] Device session list + revoke controls.
- [x] iOS migrated to generated Connect Swift unary clients for current app surfaces.
- [x] Reproducible API and iOS E2E flows.
- [x] Eval tests (`//go:build eval`) with real LLM via in-process `httptest.NewServer` — replaces standalone E2E binary.
- [x] XCUITest target (`PincerUITests`) for native iOS UI E2E testing.
- [x] `buf` is pinned in `mise` and used for Go + Swift code generation.
- [x] iOS consumes `StartTurn`/`WatchThread` for live streaming thinking/output rendering.
- [x] Inline READ tool execution loop — READ-classified tools execute during the turn without approval, results feed back into planner context.
- [x] `web_search` tool via Kagi Search API.
- [x] `web_summarize` tool via Kagi Universal Summarizer API.
- [x] Native OpenAI tool calling (function calling API) replaces `response_format: json_object` in planner.
- [x] All `run_bash` commands require explicit approval (no silent READ bypass). Matches spec §8.7.
- [x] Upgrade iOS chat markdown rendering to `Textual` for full block-level markdown support.
- [x] `web_fetch` raw URL read tool with SSRF protections (private/loopback IP blocking, redirect cap, response size cap).
- [ ] Phase 2 integrations started.

Next priority:

- [ ] Test inline tool loop end-to-end with live Kagi API.
- [x] Add web_fetch tool for raw URL content retrieval (with SSRF protections).
- [ ] Continue Phase 3 foundation on turn orchestration and bounded tool-loop planning.
