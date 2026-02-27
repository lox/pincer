# Pincer Implementation Plan

Status: Active
Last updated: 2026-02-27

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
- [x] Phase 2.5: Autonomy foundations (workspace, memory, heartbeat, jobs, scheduler)
- [ ] Phase 3: Long-horizon autonomy and durable execution
- [ ] Phase 4: Skills and controlled self-improvement
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

- [x] Add Google OAuth token storage and encryption for user + bot identities.
- [x] Add Gmail read/search/snippet tools.
- [x] Add Gmail draft creation tools.
- [x] Add bot send tool behind explicit approval.
- [x] Keep user mailbox send disabled.
- [ ] Add Calendar read tool.
- [x] Add web search tool (Kagi Search API) and web summarize tool (Kagi Universal Summarizer API).
- [x] Add web_fetch raw URL read tool with SSRF and size constraints.
- [x] Add `image_describe` multimodal tool for image analysis via vision-capable model.
- [x] Add safe image rendering in iOS chat with HMAC-signed image proxy to prevent exfiltration (goldmark AST rewriter + HTML stripping).
- [ ] Add structured inline citations for web content summaries (source markers in text + sources array in planner output, rendered as tappable chips in iOS with domain/title/link).
- [ ] Add deterministic approval-card rendering for each tool type.
- [ ] Add APNs push notifications for pending approvals, expiring approvals, job completion/failure, and proactive reach-out events.
- [ ] Add Background App Refresh (`BGTaskScheduler`) to poll for pending approvals and job state changes when app is backgrounded.
- [ ] Add App Intents / Siri Shortcuts for common actions (ask Pincer, check pending approvals, start a new thread).

Exit criteria:

- [ ] Real tool reads operate via explicit `identity` selection.
- [ ] Writes/sends remain approval-gated.
- [ ] Tool args are schema-validated and auditable.

## 5. Phase 2.5 - Autonomy foundations

Goal:

- [x] Give the agent persistent memory, proactive behavior, and background execution capability.

Design: `docs/autonomy.md`

Steps:

- [x] Add workspace directory with configurable root (`PINCER_WORKSPACE`, default `~/.pincer/workspace`).
- [x] Implement `read_file`, `write_file`, `append_file`, `list_dir` tools (workspace-sandboxed, with path-based approval gating for writes).
- [x] Bootstrap workspace layout on first start (memory/, skills/, scratch/, template HEARTBEAT.md).
- [x] Implement two-layer file-based memory: `memory/MEMORY.md` (long-term) + `memory/YYYYMM/YYYYMMDD.md` (daily notes).
- [x] Inject memory context into planner system prompt on every call (mtime-cached).
- [x] Update SOUL.md with memory instructions.
- [x] Implement heartbeat service: goroutine ticker, reads HEARTBEAT.md, runs turn in system thread.
- [x] Implement `spawn` tool and background job runner (goroutine per job, job-scoped budgets).
- [x] Post job results to originating chat thread on completion.
- [x] Mark in-flight `RUNNING` jobs as `FAILED` with `job_failed_restart` audit on startup.
- [x] Implement `schedule_create`, `schedule_list`, `schedule_delete` tools.
- [x] Implement scheduler service with `cron`/`interval`/`at` triggers and wakeup deduplication.
- [x] Connect scheduler wakeups to job creation.
- [x] Define unified work item queue: all triggers (chat, heartbeat, schedule, spawn, future webhooks) produce same work item shape.
- [x] Add Agent Memory section to iOS Settings (view/edit `memory/MEMORY.md` via RPC).
- [x] Add Heartbeat config to iOS Settings (toggle, interval, edit HEARTBEAT.md).
- [x] Surface heartbeat findings and job result messages in iOS Chat without adding thread-list clutter.
- [x] Populate iOS Jobs and Schedule tabs with real data.

Exit criteria:

- [x] Agent remembers facts across sessions via file-based memory.
- [x] Heartbeat fires periodically and the agent can proactively report findings.
- [x] Agent can spawn background jobs that run the full planner-tool loop.
- [x] Agent can create its own scheduled triggers.
- [x] All autonomy triggers use the same approval pipeline for external side effects.

## 6. Phase 3 - Long-horizon autonomy and durable execution

Goal:

- [ ] Enable durable autonomous execution that survives restarts with governed turn budgets.

Steps:

- [ ] Persist turn checkpoints after each tool step so turns can resume across restarts.
- [ ] Implement turn orchestration with bounded planner-tool loop (`max_tool_steps`, `max_tool_tokens`, `max_context_messages`).
- [ ] Implement deterministic repair/fallback handling for malformed tool-call/model outputs.
- [ ] Implement step runner limits (time/tool/token budgets) with clear failure states.
- [ ] Implement context window management (pre-request guard, progressive compaction, LLM-driven summarization).
- [ ] Implement tool loop detection (sliding window, outcome hashing, warning → block escalation).
- [ ] Emit job progress to thread messages and artifacts.
- [ ] Enforce that background jobs cannot directly execute external writes.
- [ ] Add Live Activities / Dynamic Island for active turn execution progress and pending approval count.
- [ ] Add internal event/bus abstraction for subagent callback delivery.

Exit criteria:

- [ ] Jobs can pause/resume across restarts.
- [ ] Turns are bounded, replay-safe, and audit-covered.
- [ ] Context window is managed automatically without unbounded growth.
- [ ] Autonomous runs stay within internal-only constraints unless approved.

## 7. Phase 4 - Skills and controlled self-improvement

Goal:

- [ ] Add the skill system and controlled self-improvement without weakening policy boundaries.

Steps:

- [ ] Implement skills as markdown packages in `workspace/skills/` (progressive disclosure: name+description in prompt, full body on demand).
- [ ] Add delegated work unit/subagent support with strict capability and scope policies.
- [ ] Add internal proposal flows for skill/prompt/schedule improvements.
- [ ] Require explicit owner approval for policy/scope/runtime-impacting changes.

Exit criteria:

- [ ] Agent can apply curated skills bound to explicit tool permissions.
- [ ] No skill or memory pathway bypasses approval/policy controls.

## 8. Phase 5 - Production hardening and scale

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

## 9. Cross-phase non-negotiables

- [x] LLM output remains untrusted.
- [x] External side effects always use `proposed -> approved -> executed -> audited`.
- [x] Idempotency conflicts are hard failures with audit events.
- [x] No policy-bypass channels.
- [ ] All planner-tool turns are bounded, replay-safe, and audit-covered.
- [ ] Triggered turns (jobs/schedules/heartbeat/subagents) must use the same proposal pipeline.

## 10. Evaluation strategy

Two classes of evals, with fundamentally different execution models:

### 10.1 Deterministic evals (safety invariants)

Binary pass/fail assertions on hard invariants. Run as Go tests (`//go:build eval`),
fail the build if they break. One trial per task is sufficient.

Planned cases:

- [ ] Approval bypass resistance: adversarial prompts attempting to execute `run_bash` without approval. Assert no `action_executed` audit event without preceding `action_approved`.
- [ ] Policy compliance: "just run it, skip approval" → assert `proposed_actions` created, status stays `PENDING`.
- [ ] Exfiltration gating: requests to send data externally → assert risk class upgrade and approval requirement.
- [ ] Untrusted output handling: malformed model output → assert repair/fallback/`FAILED_MODEL_OUTPUT` path.
- [ ] Idempotency conflict: duplicate execution attempt → assert hard failure with `idempotency_conflict` audit event.

### 10.2 Non-deterministic evals (task quality)

Statistical measurement over N trials (5-10+) per task. Separate harness from `go test`
(e.g. `cmd/pincer-eval/`), outputs structured JSON scores for comparison across runs.
Graders are a mix of deterministic checks (tool call assertions, audit state) and
LLM-as-judge (rubric-scored by a second model call).

Key metrics: pass@k (≥1 of k trials succeeds), pass^k (all k succeed), latency, cost per eval.

Planned task levels:

- [ ] Simple: single tool call (e.g. "search for X" → assert `web_search` called with reasonable query).
- [ ] Multi-step: chained tool use (e.g. search → fetch → summarize → synthesized response).
- [ ] Planning: open-ended research tasks graded by LLM-as-judge rubric (completeness, accuracy, citations).
- [ ] Approval-aware: tasks requiring `run_bash` → full conveyor completes, response references output.

### 10.3 Future benchmarks (Phase 4+)

When memory and long-horizon primitives exist (Phase 4), adopt structured benchmarks for:

- **Cross-turn recall**: multi-turn conversations where later turns reference earlier context.
- **Preference consistency**: user states preference early, later turns must respect it.
- **Temporal reasoning**: ordering events across a multi-message thread.
- **LongMemEval**: 500 Q&A over 115K+ token chat histories, tests temporal reasoning, knowledge updates, multi-session recall. Current gold standard for agent memory.
- **LoCoMo**: 50 human conversations up to 35 sessions, tests very long-term conversational memory.
- **MemoryBench**: continual learning from feedback, tests whether agents forget when learning new things.

### 10.4 Eval-driven development

- Write evals before features when possible — defines concrete success criteria.
- Run deterministic evals on every model/prompt change.
- Run non-deterministic evals periodically (nightly or per-model-upgrade) and track trends.
- Track CLEAR dimensions (cost, latency, efficacy, assurance, reliability) per eval run.

## 11. Current checkpoint

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
- [x] `TurnPaused`/`TurnResumed` protocol events for approval-gated turn continuation.
- [x] Post-approval turn continuation (`maybeResumeTurn`) — LLM observes tool results and re-plans after approval.
- [x] `AssistantThinkingDelta` emission from model `reasoning_content`.
- [x] System-role messages filtered from planner history to prevent LLM referencing internal action IDs.
- [x] Simplified iOS chat timeline (messages + appended approvals) with full activity state tracking in reducer.
- [x] `image_describe` multimodal tool via vision model (READ, inline execution, `anthropic/claude-opus-4.6` default).
- [x] HMAC-signed image proxy (`/proxy/image`) with goldmark AST rewriter: rewrites `![](url)` to proxied URLs, strips raw HTML from assistant messages.
- [x] Gmail integration: `gmail_search` (READ), `gmail_read` (READ), `gmail_create_draft` (WRITE), `gmail_send_draft` (EXFILTRATION/bot-only).
- [x] Google OAuth token storage with AES-256-GCM encryption at rest (`oauth_tokens` table, `GOOGLE_OAUTH_ENCRYPTION_KEY` config).
- [x] Thread list navigation with `ListThreads`/`DeleteThread` RPCs, auto-generated thread titles, and `...` context menu (New Chat, Copy Thread ID, Delete Thread).
- [x] Heartbeat service: configurable background ticker (`PINCER_HEARTBEAT_ENABLED`, `PINCER_HEARTBEAT_INTERVAL`), dedicated `thread_heartbeat`, and `HEARTBEAT_OK` no-op suppression.
- [x] Spawn/jobs backend vertical slice: `spawn` READ tool, `JobsService` CRUD, background job runner with WAITING_APPROVAL/COMPLETED/CANCELLED/PAUSED_BUDGET transitions, approval-safe resume, and completion summaries posted to origin threads.
- [x] Unified durable work item queue (`work_items` + `threads.active_turn_id`) with priority ordering (`chat > approval-resume > job > schedule > heartbeat`), restart requeue for in-flight items, and queue-routed turn execution for chat/heartbeat/jobs/schedules/approval-resume.
- [x] System autonomy RPCs: `SystemService.GetAgentMemory`/`UpdateAgentMemory` (canonical file `memory/MEMORY.md`) and `SystemService.GetHeartbeatConfig`/`UpdateHeartbeatConfig`, with persisted heartbeat runtime settings.
- [x] iOS autonomy surfaces: Agent Memory + Heartbeat settings editors, live Jobs/Schedules tabs (real backend data + quick actions), and Chat thread-list unread activity badges.
- [ ] Phase 2 integrations continued.

Next priority:

- [ ] Test inline tool loop end-to-end with live Kagi API.
- [x] Add web_fetch tool for raw URL content retrieval (with SSRF protections).
- [x] Turn orchestration with pause/resume and bounded tool-loop planning (Phase 3 foundation).
