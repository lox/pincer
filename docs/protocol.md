# Pincer ConnectRPC Streaming Protocol

Status: Active (partially implemented)
Date: 2026-02-17
References: `docs/spec.md`, `docs/ios-ui-plan.md`

This document defines a ConnectRPC + protobuf protocol for Pincer with first-class event streaming.
It targets full coverage of the canonical system contract in `docs/spec.md`.

## 0. Current Implementation Status (2026-02-14)

1. Backend control-plane APIs are served via protobuf-defined ConnectRPC handlers.
2. Turn/thread streaming is implemented for `TurnsService.StartTurn` and `EventsService.WatchThread`, including incremental command output events.
3. iOS uses generated Connect Swift unary clients for current chat/approvals/settings surfaces.
4. Proto/Go/Swift generation is wired through `buf` and checked into the repo.
5. Remaining work includes stream-first iOS chat consumption and completion of currently unimplemented Jobs/Schedules RPC methods.

## 1. Goals

1. Provide a typed protocol for all control-plane API groups in the spec.
2. Stream turn lifecycle and work-progress state to clients.
3. Support expandable thinking output in chat.
4. Support incremental command/tool output streaming (stdout/stderr chunks).
5. Preserve the side-effect conveyor: `proposed -> approved -> executed -> audited`.
6. Preserve replay safety, resumability, and auditability.

## 2. Non-goals

1. Long-term dual-stack REST + Connect support for control-plane APIs.
2. Policy behavior changes that diverge from `docs/spec.md`.
3. Default exposure of unrestricted raw chain-of-thought.

## 3. Scope and Parity Target

This protocol is intended to cover all API groups in `docs/spec.md` section 13:

1. Pairing/auth/devices
2. Chat/threads/messages
3. Approvals
4. Jobs
5. Schedules
6. System surfaces (policy summary, audit, notifications)

### 3.1 Connect-Only Direction

This repo is in early development with a single first-party client.
The target direction is full ConnectRPC control-plane cutover and REST removal for user-facing APIs.

Connect-only scope:

1. All current `/v1/...` control-plane endpoints are replaced by protobuf-defined Connect services.
2. iOS consumes generated Connect clients only for authenticated control-plane actions.
3. REST compatibility shims are temporary migration scaffolding, not a supported steady state.
4. Non-control-plane operational endpoints (for example health/metrics) may remain HTTP routes.

## 4. Transport and Security Baseline

1. Protobuf is the schema source of truth.
2. ConnectRPC over HTTPS/TLS only.
3. Bearer auth reused from existing pairing/token model.
4. Every RPC and stream is user-scoped and authenticated.
5. Stream payloads must be sanitized and secret-redacted before model-facing echo.
6. Untrusted content must be explicitly labeled in streamed and persisted artifacts.

## 5. Service Surface

1. `AuthService`
2. `DevicesService`
3. `ThreadsService`
4. `TurnsService`
5. `EventsService`
6. `ApprovalsService`
7. `JobsService`
8. `SchedulesService`
9. `SystemService`

### 5.1 Service Responsibilities

1. `AuthService`: pairing code issuance, pairing bind/token issuance, token rotation.
2. `DevicesService`: list paired devices, revoke device.
3. `ThreadsService`: create thread, fetch snapshot/history, list messages.
4. `TurnsService`: submit a turn via unary (`SendTurn`) and/or stream turn-scoped events (`StartTurn`).
5. `EventsService`: live/replay stream of thread/system events.
6. `ApprovalsService`: list pending/executed/rejected approvals and mutate approval state.
7. `JobsService`: list/create/get/cancel jobs; job state updates stream via `EventsService`.
8. `SchedulesService`: list/create/update/run-now schedules.
9. `SystemService`: policy summary, audit listing, notifications listing.

### 5.2 REST-to-RPC Replacement Map

This table defines the direct replacement target for existing REST routes.
After cutover, these REST routes are removed.

| REST route | ConnectRPC replacement |
| --- | --- |
| `POST /v1/pairing/code` | `AuthService.CreatePairingCode` |
| `POST /v1/pairing/bind` | `AuthService.BindPairingCode` |
| `GET /v1/devices` | `DevicesService.ListDevices` |
| `POST /v1/devices/{device_id}/revoke` | `DevicesService.RevokeDevice` |
| `POST /v1/chat/threads` | `ThreadsService.CreateThread` |
| `POST /v1/chat/threads/{thread_id}/messages` | `TurnsService.SendTurn` (or `TurnsService.StartTurn` for stream-first clients) |
| `GET /v1/chat/threads/{thread_id}/messages` | `ThreadsService.ListThreadMessages` |
| `GET /v1/approvals` | `ApprovalsService.ListApprovals` |
| `POST /v1/approvals/{action_id}/approve` | `ApprovalsService.ApproveAction` |
| `POST /v1/approvals/{action_id}/reject` | `ApprovalsService.RejectAction` |
| `GET /v1/jobs` | `JobsService.ListJobs` |
| `POST /v1/jobs` | `JobsService.CreateJob` |
| `GET /v1/jobs/{job_id}` | `JobsService.GetJob` |
| `POST /v1/jobs/{job_id}/cancel` | `JobsService.CancelJob` |
| `GET /v1/schedules` | `SchedulesService.ListSchedules` |
| `POST /v1/schedules` | `SchedulesService.CreateSchedule` |
| `PATCH /v1/schedules/{schedule_id}` | `SchedulesService.UpdateSchedule` |
| `POST /v1/schedules/{schedule_id}/run-now` | `SchedulesService.RunScheduleNow` |
| `GET /v1/settings/policy` | `SystemService.GetPolicySummary` |
| `GET /v1/audit` | `SystemService.ListAudit` |
| `GET /v1/notifications` | `SystemService.ListNotifications` |

## 6. Streaming Model

All streamed items use an event envelope with per-thread or per-job monotonic ordering.

Envelope requirements:

1. `event_id` stable unique identifier.
2. `thread_id` and/or `job_id` scope.
3. `sequence` monotonic uint64 (per stream scope).
4. `turn_id` when event belongs to a specific turn.
5. `occurred_at` UTC timestamp.
6. `source` (`MODEL_UNTRUSTED`, `POLICY_ENGINE`, `TOOL_EXECUTOR`, `SYSTEM`).
7. `content_trust` classification (`UNTRUSTED_MODEL`, `TRUSTED_VALIDATED`, `TRUSTED_SYSTEM`).

Delivery semantics:

1. At-least-once delivery.
2. Client dedupe by `event_id`.
3. Resume by `from_sequence`.
4. If sequence window is no longer available, emit `stream_gap` and close.
5. Streamable events are published from a durable outbox after transaction commit.

## 7. Protobuf Sketch

```proto
syntax = "proto3";

package pincer.protocol.v1;

import "google/protobuf/struct.proto";
import "google/protobuf/timestamp.proto";

option go_package = "github.com/lox/pincer/gen/pincer/protocol/v1;protocolv1";

enum QueueMode {
  QUEUE_MODE_UNSPECIFIED = 0;
  REJECT_IF_BUSY = 1;
  INTERRUPT_AFTER_CURRENT_TOOL = 2;
  QUEUE_AFTER_TURN = 3;
}

enum ReasoningVisibility {
  REASONING_VISIBILITY_UNSPECIFIED = 0;
  REASONING_OFF = 1;
  REASONING_SUMMARY = 2;
  REASONING_RAW = 3; // policy-gated; default disabled
}

enum EventSource {
  EVENT_SOURCE_UNSPECIFIED = 0;
  MODEL_UNTRUSTED = 1;
  POLICY_ENGINE = 2;
  TOOL_EXECUTOR = 3;
  SYSTEM = 4;
}

enum ContentTrust {
  CONTENT_TRUST_UNSPECIFIED = 0;
  UNTRUSTED_MODEL = 1;
  TRUSTED_VALIDATED = 2;
  TRUSTED_SYSTEM = 3;
}

enum Identity {
  IDENTITY_UNSPECIFIED = 0;
  IDENTITY_USER = 1;
  IDENTITY_BOT = 2;
  IDENTITY_NONE = 3;
}

enum RiskClass {
  RISK_CLASS_UNSPECIFIED = 0;
  READ = 1;
  WRITE = 2;
  EXFILTRATION = 3;
  DESTRUCTIVE = 4;
  HIGH = 5;
}

enum PolicyDecision {
  POLICY_DECISION_UNSPECIFIED = 0;
  ALLOW_INTERNAL = 1;
  REQUIRE_APPROVAL = 2;
  BLOCKED = 3;
}

enum ActionStatus {
  ACTION_STATUS_UNSPECIFIED = 0;
  PENDING = 1;
  APPROVED = 2;
  REJECTED = 3;
  EXECUTED = 4;
}

enum OutputStream {
  OUTPUT_STREAM_UNSPECIFIED = 0;
  STDOUT = 1;
  STDERR = 2;
}

enum TriggerType {
  TRIGGER_TYPE_UNSPECIFIED = 0;
  CHAT_MESSAGE = 1;
  JOB_WAKEUP = 2;
  SCHEDULE_WAKEUP = 3;
  HEARTBEAT = 4;
  DELEGATED_CALLBACK = 5;
}

service AuthService {
  rpc CreatePairingCode(CreatePairingCodeRequest) returns (CreatePairingCodeResponse);
  rpc BindPairingCode(BindPairingCodeRequest) returns (BindPairingCodeResponse);
  rpc RotateToken(RotateTokenRequest) returns (RotateTokenResponse);
}

service DevicesService {
  rpc ListDevices(ListDevicesRequest) returns (ListDevicesResponse);
  rpc RevokeDevice(RevokeDeviceRequest) returns (RevokeDeviceResponse);
}

service ThreadsService {
  rpc CreateThread(CreateThreadRequest) returns (CreateThreadResponse);
  rpc GetThreadSnapshot(GetThreadSnapshotRequest) returns (GetThreadSnapshotResponse);
  rpc ListThreadMessages(ListThreadMessagesRequest) returns (ListThreadMessagesResponse);
}

service TurnsService {
  // Unary turn submission for clients that do not consume server-stream framing.
  rpc SendTurn(SendTurnRequest) returns (SendTurnResponse);
  // Submit one turn and stream events until terminal turn event.
  rpc StartTurn(StartTurnRequest) returns (stream ThreadEvent);
}

service EventsService {
  // Live/replay stream for thread-level updates (messages, approvals, execution, job links).
  rpc WatchThread(WatchThreadRequest) returns (stream ThreadEvent);
}

service ApprovalsService {
  rpc ListApprovals(ListApprovalsRequest) returns (ListApprovalsResponse);
  rpc ApproveAction(ApproveActionRequest) returns (ApproveActionResponse);
  rpc RejectAction(RejectActionRequest) returns (RejectActionResponse);
}

service JobsService {
  rpc ListJobs(ListJobsRequest) returns (ListJobsResponse);
  rpc CreateJob(CreateJobRequest) returns (CreateJobResponse);
  rpc GetJob(GetJobRequest) returns (GetJobResponse);
  rpc CancelJob(CancelJobRequest) returns (CancelJobResponse);
}

service SchedulesService {
  rpc ListSchedules(ListSchedulesRequest) returns (ListSchedulesResponse);
  rpc CreateSchedule(CreateScheduleRequest) returns (CreateScheduleResponse);
  rpc UpdateSchedule(UpdateScheduleRequest) returns (UpdateScheduleResponse);
  rpc RunScheduleNow(RunScheduleNowRequest) returns (RunScheduleNowResponse);
}

service SystemService {
  rpc GetPolicySummary(GetPolicySummaryRequest) returns (GetPolicySummaryResponse);
  rpc ListAudit(ListAuditRequest) returns (ListAuditResponse);
  rpc ListNotifications(ListNotificationsRequest) returns (ListNotificationsResponse);
}

message CreatePairingCodeRequest {}
message CreatePairingCodeResponse {
  string code = 1;
  google.protobuf.Timestamp expires_at = 2;
}

message BindPairingCodeRequest {
  string code = 1;
  string device_name = 2;
  string public_key = 3;
}

message BindPairingCodeResponse {
  string device_id = 1;
  string token = 2;
  google.protobuf.Timestamp expires_at = 3;
  google.protobuf.Timestamp renew_after = 4;
}

message RotateTokenRequest {}
message RotateTokenResponse {
  string token = 1;
  google.protobuf.Timestamp expires_at = 2;
  google.protobuf.Timestamp renew_after = 3;
}

message Device {
  string device_id = 1;
  string name = 2;
  bool is_current = 3;
  google.protobuf.Timestamp created_at = 4;
  google.protobuf.Timestamp revoked_at = 5;
}

message ListDevicesRequest {}
message ListDevicesResponse {
  repeated Device items = 1;
}

message RevokeDeviceRequest {
  string device_id = 1;
}

message RevokeDeviceResponse {
  string device_id = 1;
}

message CreateThreadRequest {}
message CreateThreadResponse {
  string thread_id = 1;
  uint64 last_sequence = 2;
}

message TurnBudget {
  uint32 max_tool_steps = 1;
  uint32 max_tool_tokens = 2;
  uint32 max_context_messages = 3;
}

message StartTurnRequest {
  string thread_id = 1;
  string client_message_id = 2; // client idempotency key
  string user_text = 3;
  QueueMode queue_mode = 4;
  ReasoningVisibility reasoning_visibility = 5;
  TriggerType trigger_type = 6; // chat/job/schedule/heartbeat/delegated
  string trigger_source_id = 7; // job_id/schedule_id/callback_id as applicable
  TurnBudget requested_budget = 8; // optional override within server-enforced bounds
  uint64 resume_from_sequence = 9;
}

message WatchThreadRequest {
  string thread_id = 1;
  uint64 from_sequence = 2; // exclusive lower bound
}

message GetThreadSnapshotRequest {
  string thread_id = 1;
}

message ListThreadMessagesRequest {
  string thread_id = 1;
  uint32 page_size = 2;
  string page_token = 3;
}

message ThreadMessage {
  string message_id = 1;
  string role = 2; // user|assistant|system
  string content = 3;
  google.protobuf.Struct metadata = 4;
  ContentTrust content_trust = 5;
  google.protobuf.Timestamp created_at = 6;
}

message GetThreadSnapshotResponse {
  string thread_id = 1;
  uint64 last_sequence = 2;
  repeated ThreadMessage messages = 3;
}

message ListThreadMessagesResponse {
  repeated ThreadMessage items = 1;
  string next_page_token = 2;
  uint64 last_sequence = 3;
}

message ThreadEvent {
  string event_id = 1;
  string thread_id = 2;
  string job_id = 3;
  string turn_id = 4; // deterministic turn identifier
  uint64 sequence = 5;
  google.protobuf.Timestamp occurred_at = 6;
  EventSource source = 7;
  ContentTrust content_trust = 8;

  oneof payload {
    TurnStarted turn_started = 20;
    TurnBudgetApplied turn_budget_applied = 21;
    ModelOutputRepairAttempted model_output_repair_attempted = 22;
    TurnCompleted turn_completed = 23;
    TurnFailed turn_failed = 24;
    TurnPaused turn_paused = 25;
    TurnResumed turn_resumed = 26;

    AssistantThinkingDelta assistant_thinking_delta = 30;
    AssistantTextDelta assistant_text_delta = 31;
    AssistantMessageCommitted assistant_message_committed = 32;

    ToolCallPlanned tool_call_planned = 40;
    ToolExecutionStarted tool_execution_started = 41;
    ToolExecutionOutputDelta tool_execution_output_delta = 42;
    ToolExecutionFinished tool_execution_finished = 43;

    PolicyDecisionMade policy_decision_made = 50;
    ProposedActionCreated proposed_action_created = 51;
    ProposedActionStatusChanged proposed_action_status_changed = 52;
    IdempotencyConflict idempotency_conflict = 53;

    JobStatusChanged job_status_changed = 60;
    ScheduleTriggered schedule_triggered = 61;
    DelegatedCallbackReceived delegated_callback_received = 62;

    AuditEventRecorded audit_event_recorded = 70;
    NotificationQueued notification_queued = 71;
    ArtifactCreated artifact_created = 72;
    MemoryCheckpointSaved memory_checkpoint_saved = 73;
    SkillProposalCreated skill_proposal_created = 74;
    SelfImprovementProposalCreated self_improvement_proposal_created = 75;

    Heartbeat heartbeat = 90;
    StreamGap stream_gap = 91;
  }
}

message TurnStarted {
  string user_message_id = 1;
  TriggerType trigger_type = 2;
}

message TurnBudgetApplied {
  TurnBudget effective_budget = 1;
}

message ModelOutputRepairAttempted {
  uint32 repair_attempt = 1; // 1..N
  bool fallback_model_used = 2;
}

message TurnCompleted {
  string assistant_message_id = 1;
}

message TurnFailed {
  string code = 1; // includes FAILED_MODEL_OUTPUT and budget violations
  string message = 2;
  bool retryable = 3;
}

message TurnPaused {
  uint32 pending_action_count = 1;
  uint32 steps_used = 2;
  uint32 steps_remaining = 3;
}

message TurnResumed {
  string resumed_reason = 1;
  uint32 steps_remaining = 2;
}

message AssistantThinkingDelta {
  string segment_id = 1;
  string delta = 2;
  bool redacted = 3;
  ReasoningVisibility visibility = 4;
}

message AssistantTextDelta {
  string segment_id = 1;
  string delta = 2;
}

message AssistantMessageCommitted {
  string message_id = 1;
  string full_text = 2;
  google.protobuf.Struct metadata = 3;
}

message ToolCallPlanned {
  string tool_call_id = 1;
  string tool_name = 2;
  google.protobuf.Struct args = 3;
  RiskClass risk_class = 4;
  Identity identity = 5;
}

message ToolExecutionStarted {
  string execution_id = 1;
  string tool_call_id = 2;
  string tool_name = 3;
  string display_command = 4;
}

message ToolExecutionOutputDelta {
  string execution_id = 1;
  OutputStream stream = 2;
  bytes chunk = 3;
  uint64 offset_bytes = 4;
  bool utf8 = 5;
}

message ToolExecutionFinished {
  string execution_id = 1;
  int32 exit_code = 2;
  uint64 duration_ms = 3;
  bool timed_out = 4;
  bool truncated = 5;
}

message PolicyDecisionMade {
  string policy_id = 1;
  PolicyDecision decision = 2;
  string reason = 3; // e.g. untrusted_ingest_block, requires_approval, ssrf_blocked
}

message ProposedActionCreated {
  string action_id = 1;
  string tool = 2;
  RiskClass risk_class = 3;
  Identity identity = 4;
  string idempotency_key = 5;
  string justification = 6;
  string deterministic_summary = 7;
  google.protobuf.Struct preview = 8; // target/diff/recipient/domain, etc.
  google.protobuf.Timestamp expires_at = 9;
}

message ProposedActionStatusChanged {
  string action_id = 1;
  ActionStatus status = 2;
  string reason = 3;
}

message IdempotencyConflict {
  string action_id = 1;
  string tool = 2;
  string idempotency_key = 3;
  string reason = 4; // args_hash_mismatch
}

message JobStatusChanged {
  string job_id = 1;
  string status = 2;
}

message ScheduleTriggered {
  string schedule_id = 1;
  google.protobuf.Timestamp scheduled_for_utc = 2;
}

message DelegatedCallbackReceived {
  string callback_id = 1;
}

message AuditEventRecorded {
  string entry_id = 1;
  string event_type = 2;
}

message NotificationQueued {
  string notification_id = 1;
  string type = 2;
}

message ArtifactCreated {
  string artifact_id = 1;
  string thread_id = 2;
  string media_type = 3;
}

message MemoryCheckpointSaved {
  string checkpoint_id = 1;
  string scope = 2; // thread|job
}

message SkillProposalCreated {
  string proposal_id = 1;
  string title = 2;
  bool requires_owner_approval = 3;
}

message SelfImprovementProposalCreated {
  string proposal_id = 1;
  string kind = 2; // policy|scope|runtime
  bool requires_owner_approval = 3;
}

message Heartbeat {
  uint64 latest_sequence = 1;
}

message StreamGap {
  uint64 requested_from_sequence = 1;
  uint64 next_available_sequence = 2;
}

message Approval {
  string action_id = 1;
  string source = 2;
  string source_id = 3;
  string tool = 4;
  ActionStatus status = 5;
  RiskClass risk_class = 6;
  Identity identity = 7;
  string deterministic_summary = 8;
  google.protobuf.Struct preview = 9;
  google.protobuf.Timestamp created_at = 10;
  google.protobuf.Timestamp expires_at = 11;
  string rejection_reason = 12;
}

message ListApprovalsRequest {
  ActionStatus status = 1;
}

message ListApprovalsResponse {
  repeated Approval items = 1;
}

message ApproveActionRequest {
  string action_id = 1;
}

message ApproveActionResponse {
  string action_id = 1;
  ActionStatus status = 2;
}

message RejectActionRequest {
  string action_id = 1;
  string reason = 2;
}

message RejectActionResponse {
  string action_id = 1;
  ActionStatus status = 2;
}

enum JobStatus {
  JOB_STATUS_UNSPECIFIED = 0;
  JOB_RUNNING = 1;
  JOB_WAITING_APPROVAL = 2;
  JOB_COMPLETED = 3;
  JOB_FAILED = 4;
  JOB_PAUSED_BUDGET = 5;
  JOB_CANCELLED = 6;
}

enum ScheduleTriggerKind {
  SCHEDULE_TRIGGER_KIND_UNSPECIFIED = 0;
  SCHEDULE_TRIGGER_CRON = 1;
  SCHEDULE_TRIGGER_INTERVAL = 2;
  SCHEDULE_TRIGGER_AT = 3;
}

enum NotificationType {
  NOTIFICATION_TYPE_UNSPECIFIED = 0;
  NOTIFICATION_PENDING_APPROVAL_CREATED = 1;
  NOTIFICATION_APPROVAL_EXPIRING_SOON = 2;
  NOTIFICATION_JOB_COMPLETED = 3;
  NOTIFICATION_JOB_FAILED = 4;
  NOTIFICATION_PROACTIVE_REACH_OUT = 5;
}

message Job {
  string job_id = 1;
  string goal = 2;
  JobStatus status = 3;
  string thread_id = 4;
  TriggerType trigger_type = 5;
  string trigger_source_id = 6;
  TurnBudget budget = 7;
  uint64 max_wall_time_ms = 8;
  string last_error = 9;
  google.protobuf.Timestamp created_at = 10;
  google.protobuf.Timestamp updated_at = 11;
}

message ListJobsRequest {}
message ListJobsResponse {
  repeated Job items = 1;
}

message CreateJobRequest {
  string goal = 1;
  TurnBudget budget = 2;
  uint64 max_wall_time_ms = 3;
}

message CreateJobResponse {
  Job item = 1;
}

message GetJobRequest {
  string job_id = 1;
}

message GetJobResponse {
  Job item = 1;
}

message CancelJobRequest {
  string job_id = 1;
}

message CancelJobResponse {
  string job_id = 1;
  JobStatus status = 2;
}

message Schedule {
  string schedule_id = 1;
  string name = 2;
  ScheduleTriggerKind trigger_kind = 3;
  string trigger_spec = 4;
  string timezone = 5; // IANA
  bool enabled = 6;
  google.protobuf.Timestamp next_run_at = 7;
  google.protobuf.Timestamp last_run_at = 8;
  google.protobuf.Timestamp created_at = 9;
  google.protobuf.Timestamp updated_at = 10;
}

message ListSchedulesRequest {}
message ListSchedulesResponse {
  repeated Schedule items = 1;
}

message CreateScheduleRequest {
  string name = 1;
  ScheduleTriggerKind trigger_kind = 2;
  string trigger_spec = 3;
  string timezone = 4; // IANA name
}

message CreateScheduleResponse {
  Schedule item = 1;
}

message UpdateScheduleRequest {
  string schedule_id = 1;
  google.protobuf.Struct patch = 2;
}

message UpdateScheduleResponse {
  Schedule item = 1;
}

message RunScheduleNowRequest {
  string schedule_id = 1;
}

message RunScheduleNowResponse {
  string schedule_id = 1;
  string wakeup_event_id = 2;
  string job_id = 3;
  string turn_id = 4;
}

message GetPolicySummaryRequest {}
message GetPolicySummaryResponse {
  google.protobuf.Struct summary = 1;
  string policy_version = 2;
}

message AuditEntry {
  string entry_id = 1;
  string event_type = 2;
  string thread_id = 3;
  string job_id = 4;
  string action_id = 5;
  google.protobuf.Struct payload = 6;
  google.protobuf.Timestamp occurred_at = 7;
}

message ListAuditRequest {}
message ListAuditResponse {
  repeated AuditEntry items = 1;
}

message Notification {
  string notification_id = 1;
  NotificationType type = 2;
  string resource_kind = 3; // thread|job|action
  string resource_id = 4;
  google.protobuf.Timestamp created_at = 5;
  google.protobuf.Timestamp read_at = 6;
}

message ListNotificationsRequest {}
message ListNotificationsResponse {
  repeated Notification items = 1;
}
```

## 8. Semantic Contracts

### 8.1 Planner/Tool Loop and Model Output Repair

1. Turn execution uses bounded planner/tool loop semantics.
2. Planner output must normalize to assistant text, optional tool calls, and normalized proposed actions.
3. If tool calls are present, loop execution continues until a terminal turn state.
4. Invalid model output handling must follow repair then fallback then `FAILED_MODEL_OUTPUT`.
5. `ModelOutputRepairAttempted` and `TurnFailed(code="FAILED_MODEL_OUTPUT")` make this visible to clients and audit.

### 8.2 Thinking Output

1. Thinking is streamed through `AssistantThinkingDelta`.
2. Default is `REASONING_SUMMARY`.
3. `REASONING_RAW` is explicit and policy-gated.
4. Thinking is never policy authority; trusted code owns policy/execution decisions.

### 8.3 Incremental Command Output

1. `ToolExecutionStarted` creates the UI execution card.
2. `ToolExecutionOutputDelta` appends stdout/stderr chunks in offset order.
3. `ToolExecutionFinished` provides terminal execution metadata (`exit_code`, `timed_out`, `truncated`).

### 8.4 Policy and Approval Conveyor

1. External `WRITE` and `EXFILTRATION` decisions emit `PolicyDecisionMade(decision=REQUIRE_APPROVAL)`.
2. Proposed actions emit `ProposedActionCreated`.
3. Approval mutations emit `ProposedActionStatusChanged`.
4. Canonical status set is `PENDING`, `APPROVED`, `REJECTED`, `EXECUTED`.
5. Expiry and auto-reject are represented as `REJECTED` with reason `expired`.
6. Background jobs cannot directly execute external writes/sends; they can only propose.
7. Untrusted-ingest turns cannot execute external write/send in the same turn.
8. Deterministic approval summaries and preview payloads are server-rendered.

### 8.5 Web and Shell Guardrails

1. Web fetch tools must enforce SSRF protections (no local/private targets).
2. Web fetch must cap redirects and byte budgets.
3. `run_bash` is explicit-approval only and bounded by timeout/output limits.
4. `run_bash` args are `command` (required), optional `cwd`, and optional `timeout_ms`.
5. `timeout_ms` is server-bounded (default `10_000`, max `900_000`).
6. Streamed command output is observational only and does not imply side-effect approval.

### 8.6 Identity and Risk Contracts

1. Integration tool plans include explicit `Identity` on every relevant tool call.
2. Risk classification is explicit via `RiskClass`.
3. Policy decisions reference risk and identity fields and are auditable.
4. Baseline tool families include Gmail (user/bot), Calendar, Web (`search`/`open`), bounded `run_bash`, and internal memory/artifact tools.

### 8.7 Idempotency Contract

1. External side effects require `idempotency_key`.
2. Args/result hashes are persisted server-side.
3. Hash mismatch emits `IdempotencyConflict` and audit event.

### 8.8 Turn Safety and Replay

1. Effective turn limits are surfaced with `TurnBudgetApplied`.
2. Every turn has deterministic `turn_id`.
3. Event streams are replay-safe and resumable via sequence.
4. Turn outcomes persist and can resume after process restart.

### 8.9 Jobs, Schedules, and Delegated Work

1. Each trigger enqueues a deterministic work item before turn execution.
2. `TriggerType` covers chat/job/schedule/heartbeat/delegated callback triggers.
3. Scheduler triggers carry UTC execution time; authoring timezone is IANA.
4. Scheduler trigger kinds map to `cron`, `interval`, and `at`.
5. Wakeups are deduplicated and durable.
6. Job and schedule state changes are surfaced as events.
7. Delegated callbacks re-enter the same event stream.
8. Delegated execution must remain capability and scope constrained by trusted policy.

### 8.10 Auth and Device Sessions

1. Pairing codes are short-lived and include explicit expiry.
2. Bearer token material is opaque and server-validated.
3. Token leases include TTL and sliding renewal via `renew_after`.
4. Device revocation invalidates future RPCs and active streams for that device.
5. Token material is stored as hashed/HMAC-validated server state, not plaintext bearer secrets.
6. Token and refresh-secret material must be protected at rest.

### 8.11 Memory and Artifacts

1. Thread and job checkpoints are internal trusted artifacts (`MemoryCheckpointSaved`).
2. Artifact writes are internal and do not bypass side-effect approval policy.
3. Message/checkpoint/artifact payload sizes remain bounded by server limits.

### 8.12 Skills and Self-Improvement

1. Skills are curated and constrained by explicit tool permissions.
2. Skill/self-improvement outputs are internal proposals by default.
3. Policy/scope/runtime-impacting changes require explicit owner approval before activation.

### 8.13 Notifications

1. Intervention and proactive reach-out notifications are retrievable through `SystemService`.
2. Queueing of notification intents can be streamed as `NotificationQueued`.
3. Rate limiting and policy gating are server-enforced.

### 8.14 Provider Contract

1. Provider interface is OpenAI-compatible chat with tool-calling support.
2. Provider calls include deterministic timeout and retry controls.
3. Provider fallback chains are explicit and auditable when used.

## 9. Persistence, Constraints, and Retention

The protocol does not replace storage constraints from `docs/spec.md`; server implementation must enforce:

1. SQLite remains the system of record.
2. Core data entities remain covered: `users`, `devices`, `auth_tokens`, `oauth_tokens`, `threads`, `messages`, `jobs`, `job_events`, `schedules`, `wakeup_events`, `proposed_actions`, `idempotency`, `artifacts`, `audit_log`.
3. `proposed_actions` uniqueness on `(user_id, tool, idempotency_key)`.
4. `idempotency` primary key on `(owner_id, tool_name, key)`.
5. Bounded payload sizes for message/checkpoint/artifact blobs.
6. UTC/RFC3339 durable timestamps for ordering.
7. Retention defaults are `idempotency=90d`, `audit=90d`, `artifacts=90d`, `messages=30d`.

## 10. iOS Control-Plane Mapping

Chat:

1. `TurnStarted`/`TurnCompleted`/`TurnFailed`/`TurnPaused`/`TurnResumed` drives activity indicators.
2. `AssistantThinkingDelta` feeds expandable "Thinking" panel.
3. `AssistantTextDelta` supports incremental assistant rendering.
4. Tool execution events render live terminal output cards.

Approvals:

1. `ProposedActionCreated` updates pending badge/queue.
2. `ListApprovals` + status change events keep queue synchronized.
3. Deterministic summary + preview fields back approval detail UX.
4. Approval UI can require biometric confirmation before mutate RPCs.

Work and Schedules:

1. `JobStatusChanged` supports Work tab timelines.
2. `ScheduleTriggered` supports Schedules visibility.

Settings:

1. `ListDevices` and `RevokeDevice` satisfy device session controls.
2. `GetPolicySummary` and `ListAudit` satisfy trust surfaces.

## 11. Connect-Only Cutover Plan

Current phase status:

1. Phase A and Phase B are complete for the currently implemented service set.
2. Phase C is in progress (iOS unary cutover complete; stream-first chat rendering pending).
3. Phase D is complete for app-facing control-plane route registration.
4. Phase E stabilization remains in progress.

### 11.1 Cutover Rules

1. Protobuf definitions are the only API contract source of truth.
2. New backend features must land in Connect services only.
3. No new REST handlers are added after cutover starts.
4. REST routes are removed once first-party iOS reaches Connect parity.

### 11.2 Phase A: Contract Freeze and Parity Matrix

1. Freeze REST request/response shape as migration baseline.
2. Finalize protobuf packages, method names, and event envelopes.
3. Add a parity matrix test plan using section 5.2 route replacements.
4. Define cutover telemetry for stream gaps, reconnects, and handler errors.

Exit criteria:

1. All control-plane routes map to a Connect RPC in section 5.2.
2. iOS client generation succeeds from pinned proto definitions.
3. Parity test plan exists for every replaced route and stream surface.

### 11.3 Phase B: Backend Connect Implementation

1. Implement Connect handlers for auth, devices, threads, approvals, jobs, schedules, and system services.
2. Implement `TurnsService.SendTurn`, `TurnsService.StartTurn`, and `EventsService.WatchThread`.
3. Reuse current trusted domain services so policy/idempotency/audit logic remains centralized.
4. Add authentication interceptors and shared authorization checks for unary and stream RPCs.
5. Add Connect integration tests that verify parity with the side-effect conveyor and audit events.

Exit criteria:

1. Connect handlers pass all parity and security invariants.
2. Stream resume and dedupe semantics are verified with deterministic tests.
3. No control-plane behavior depends on REST-only middleware.

### 11.4 Phase C: iOS Client Cutover

1. Replace REST transport calls with generated Connect client calls.
2. Move chat send/receive to `TurnsService.SendTurn` for unary-first clients, and `TurnsService.StartTurn` + `EventsService.WatchThread` for stream-first clients.
3. Move approvals/jobs/schedules/settings tabs to unary Connect services.
4. Validate UX surfaces for thinking panel, command output streaming, and approval state transitions.
5. Remove all REST client code paths from iOS once Connect parity is reached.

Exit criteria:

1. iOS uses Connect only for authenticated control-plane operations.
2. Chat streaming and approval conveyor remain functionally equivalent or better.
3. E2E flow passes with REST handlers disabled in server runtime.

### 11.5 Phase D: REST Removal

1. Delete REST route registration for all `/v1/...` control-plane routes.
2. Delete REST DTO types, handlers, and route tests superseded by Connect equivalents.
3. Delete REST-specific API scripts and docs; replace with Connect-oriented test harnesses.
4. Keep only non-control-plane operational endpoints (for example health and metrics).
5. Update `docs/spec.md` API surface contract from REST routes to Connect service/method definitions.
6. Remove feature flags or fallback toggles that re-enable REST control-plane paths.

Exit criteria:

1. No `/v1/...` control-plane endpoint remains in server route tables.
2. CI and E2E use Connect only for app-facing flows.
3. Docs reference ConnectRPC as the sole control-plane transport.

### 11.6 Phase E: Stabilization

1. Track stream reliability metrics (reconnect rate, gap rate, event lag) in CI and local E2E.
2. Run failure-injection tests for dropped streams and resumptions.
3. Tune output chunk cadence and replay window defaults from measured behavior.
4. Lock protobuf versioning policy for future additive-only evolution.

Exit criteria:

1. Stream reliability SLOs are met in repeated E2E runs.
2. Replay and resume behavior is deterministic under fault tests.
3. API evolution policy is documented and enforced.

### 11.7 Suggested Service Implementation Order

1. `TurnsService.SendTurn`, `TurnsService.StartTurn`, and `EventsService.WatchThread` first, because chat delivery and progress visibility are primary UX blockers.
2. `ApprovalsService` second, to preserve `proposed -> approved -> executed -> audited` control-plane flow.
3. `ThreadsService` third, to complete historical message and snapshot parity.
4. `AuthService` and `DevicesService` fourth, to migrate pairing/session controls.
5. `JobsService`, `SchedulesService`, and `SystemService` last, after core chat and approvals are stable.

### 11.8 Verification Gates During Cutover

1. Contract gate: protobuf lint/breaking-change checks pass before merge.
2. Behavior gate: Connect integration tests cover all policy and idempotency invariants.
3. Streaming gate: reconnect/resume tests validate monotonic ordering and duplicate suppression.
4. Security gate: auth interceptor tests verify unauthorized stream and unary rejection paths.
5. E2E gate: app-level happy-path and approval-path tests pass with REST control-plane routes disabled.

### 11.9 Hard Removal Checklist

1. Remove `/v1/...` control-plane route registration from server bootstrapping.
2. Remove REST handler packages and related request/response model types.
3. Replace REST-based E2E scripts with Connect client or Connect curl equivalents.
4. Remove documentation references instructing client use of REST control-plane endpoints.
5. Confirm `rg \"/v1/\"` shows only operational, non-control-plane routes or historical changelog references.

## 12. Coverage Checklist Against `docs/spec.md`

| Spec area | Protocol coverage |
| --- | --- |
| Core invariants and trust model | `EventSource`, `ContentTrust`, policy events, audited conveyor |
| Architecture (ingress + outbox) | trigger-to-turn enqueue semantics + durable outbox streaming |
| Identity and auth model | `AuthService`, `DevicesService`, explicit `Identity` on tool/action records |
| Auth lease + revocation semantics | pairing expiry, `renew_after`, `RotateToken`, device revocation |
| Data model constraints | Explicitly required in section 9 |
| Tool system and risk classes | `RiskClass`, tool planning/execution events |
| Policy rules | Approval gating plus job propose-only, untrusted-ingest block, SSRF and `run_bash` guardrails |
| Approval lifecycle | `ProposedActionCreated` + `ProposedActionStatusChanged` |
| Idempotency contract | `client_message_id`, action idempotency key, `IdempotencyConflict` |
| Turn safety controls | `turn_id`, `TurnBudgetApplied`, `TurnFailed` codes |
| Jobs/scheduler/autonomy primitives | `TriggerType`, Jobs/Schedules services, delegated callback events |
| Memory model | `MemoryCheckpointSaved`, `ArtifactCreated`, bounded retention section |
| Skills/self-improvement | proposal events + owner-approval semantics |
| Provider contract and invalid output handling | OpenAI-compatible tool-calling with retry/timeout/fallback visibility |
| API surface parity and replacement | full route-to-RPC mapping in section 5.2 with REST control-plane removal |
| iOS control-plane contract | deterministic approval summaries, streaming chat/tool progress, device/policy/audit surfaces |
| Security checklist | TLS-only, untrusted labeling, redaction, auditable decisions, replay-safe sequencing |
| Deliberate exclusions | no policy bypass, no unrestricted shell path, no hidden side-effect path |

## 13. Deliberate Exclusions

1. No unrestricted subprocess/shell execution path.
2. No policy bypass pathways.
3. No silent recipient/domain allowlist execution.
4. No hidden side-effect channels.

## 14. Open Decisions

1. Policy default for `REASONING_RAW` (recommended: disabled).
2. Event replay retention window and sequence compaction policy.
3. Chunk size and flush cadence for `ToolExecutionOutputDelta`.
4. Whether notifications should also be stream-subscribable from `EventsService`.
5. Whether to split thread-scoped and global streams in v1 or v2.
