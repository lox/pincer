# Activity Model

Status: Proposed
Date: 2026-02-16
References: `docs/spec.md`, `docs/protocol.md`, `PLAN.md`

This document defines the architecture for real-time tool activity visibility in the iOS chat UI.

## 1. Problem

The iOS UI only shows activity for `run_bash` (terminal card with streaming output). Inline READ tools (`web_search`, `web_summarize`, `web_fetch`) execute silently with no user-visible feedback. There is no generic "what is the agent doing?" indicator, and the current flat `Message` model cannot cleanly represent the state-machine nature of tool execution (planned → running → done).

As more tools are added (Phase 2: Gmail, Calendar, image_describe; Phase 3: jobs, schedulers, bounded loops; Phase 4: memory, skills, delegation), the UI needs a generic activity feed that works for any tool with minimal per-tool wiring.

## 2. Goals

1. Show real-time activity for every tool call: planned, waiting approval, running, succeeded, failed.
2. Make adding a new tool require only backend event emission (+ optional custom iOS renderer).
3. Support Phase 3/4 constructs (multi-step loops, jobs, delegation) without architectural changes.
4. Keep the conversation transcript (`Message`) clean and separate from transient activity state.

## 3. Non-goals

1. Nested activity scopes (parent/child for delegation). Defer until Phase 4.
2. Persisted activity history. Activities are reconstructed from the event stream.
3. Global cross-thread activity timeline. Scoped to per-thread for now.

## 4. Backend: event emission gaps

### 4.1 Emit `ToolCallPlanned`

The `ToolCallPlanned` message exists in the protobuf schema but is never emitted.

Where: in `executeTurn()`, right after `planTurn()` returns, before `splitByRiskClass`.

Emit one `ToolCallPlanned` event per tool call in the plan:

- `tool_call_id`: a stable ID that correlates with later `ProposedActionCreated.action_id` and `ToolExecutionStarted.tool_call_id`.
- `tool_name`: the tool name.
- `args`: the tool arguments (as `google.protobuf.Struct`).
- `risk_class`: the classified risk level.
- `identity`: if applicable.

This enables the UI to show "tool calls being planned" before execution starts, even if execution never happens (blocked, rejected, budget reached).

### 4.2 Emit `ToolExecution*` for inline READ tools

Today inline READ tools execute in `executeInlineReadTool()` and only write a hidden system message. No `ToolExecution*` events are emitted.

Change: wrap inline READ execution with the existing event emission helpers:

- `emitToolExecutionStarted(threadID, turnID, executionID, toolCallID, toolName, displayCommand)`
- `emitToolExecutionOutputDelta(threadID, turnID, executionID, stream, chunk, offset)` — for chunked output
- `emitToolExecutionFinished(threadID, turnID, executionID, result)` — exit code 0 for success, 1 for error

The helpers already exist in `action_events.go`. The `emitToolExecutionFinished` helper currently takes a `bashExecutionResult`; generalize it to accept a simpler result struct or add a parallel helper for non-bash tools.

### 4.3 Inline READ tool-result messages

Inline READ tool results are stored with `role=internal` and excluded from the user-visible chat transcript. The planner sees them via raw message queries, but `GetThreadSnapshot` / `ListThreadMessages` filter them out.

## 5. iOS: activity data model

### 5.1 Introduce `TurnActivity` (parallel to `Message`)

Activities are transient state machines, not conversation transcript entries. Keep `Message` as the clean conversation record and add a parallel model:

```swift
struct TurnActivity: Identifiable {
    let turnID: String
    var status: TurnStatus          // .running, .completed, .failed
    var thinking: ThinkingState     // streaming text, collapsed/expanded
    var toolCalls: [ToolCallActivity]
    var startedAt: Date?
    var endedAt: Date?

    var id: String { turnID }
}

enum TurnStatus {
    case running
    case completed
    case failed(message: String)
}

struct ThinkingState {
    var text: String = ""
    var isStreaming: Bool = false
}

struct ToolCallActivity: Identifiable {
    let toolCallID: String
    var toolName: String
    var displayLabel: String        // e.g. "web_fetch https://example.com"
    var argsPreview: String?        // from ToolCallPlanned args or ProposedActionCreated preview
    var state: ToolCallState
    var executions: [ToolExecutionState]

    var id: String { toolCallID }
}

enum ToolCallState {
    case planned
    case waitingApproval
    case running
    case succeeded
    case failed
    case rejected(reason: String)
}

struct ToolExecutionState: Identifiable {
    let executionID: String
    var stdout: String = ""
    var stderr: String = ""
    var exitCode: Int?
    var durationMs: UInt64?
    var isStreaming: Bool = true
    var truncated: Bool = false

    var id: String { executionID }
}
```

### 5.2 Unified chat timeline

Messages are rendered in server order with pending approvals appended at the end:

```swift
enum ChatTimelineItem: Identifiable {
    case message(Message)
    case approval(Approval)

    var id: String {
        switch self {
        case .message(let m): return "msg_\(m.id)"
        case .approval(let a): return "apv_\(a.id)"
        }
    }
}
```

The `ChatView` renders `ForEach(timelineItems)` instead of `ForEach(messages)`.

### 5.3 Event reducer: extend `ThreadEventReducerState`

Add to `ThreadEventReducerState`:

- `turnActivitiesByTurnID: [String: TurnActivity]`
- `activeTurnID: String?`
- `toolCallIDByExecutionID: [String: String]`

Event-to-activity mapping:

| Event | Activity effect |
|-------|----------------|
| `TurnStarted` | Create `TurnActivity(turnID)`, set `activeTurnID` |
| `AssistantThinkingDelta` | Append to `TurnActivity.thinking.text` |
| `ToolCallPlanned` | Upsert `ToolCallActivity` with state `.planned` |
| `ProposedActionCreated` | Upsert tool call, attach `argsPreview`, set `.waitingApproval` |
| `ProposedActionStatusChanged(APPROVED)` | Set tool call state `.planned` (ready for execution) |
| `ProposedActionStatusChanged(REJECTED)` | Set tool call state `.rejected(reason)` |
| `ToolExecutionStarted` | Find tool call via `tool_call_id`, add execution, set `.running` |
| `ToolExecutionOutputDelta` | Append to execution stdout/stderr |
| `ToolExecutionFinished` | Set exit/duration, transition to `.succeeded` or `.failed` |
| `TurnCompleted` | Set activity `.completed`, clear `activeTurnID` |
| `TurnFailed` | Set activity `.failed(message)`, clear `activeTurnID` |

The existing `Message`-focused reducer logic (`assistantTextDelta`, `assistantMessageCommitted`, etc.) continues unchanged.

## 6. iOS: UI component hierarchy

### 6.1 `ActivityCard`

A single compact card inserted into the chat timeline for each turn:

```
┌─────────────────────────────────────┐
│ Activity                    Running │
│                                     │
│ ▶ Thinking                          │
│   (collapsed disclosure group)      │
│                                     │
│ ✓ web_search "pincer security"      │
│ ✓ web_fetch https://example.com     │
│ ▶ run_bash                 Running  │
│   $ curl -s https://api.example.com │
│   {"status": "ok", ...}             │
│ ⏳ gmail_send_draft    Needs approval│
│   [Approve] [Reject]                │
└─────────────────────────────────────┘
```

Components:

- **Header**: "Activity" label + status pill (Running / Done / Failed) + spinner if running.
- **Thinking section**: collapsible `DisclosureGroup`, reuses existing `ThinkingMessageCard` internals.
- **ToolCallRow** (per tool call): icon + tool name + display label + state indicator.
  - State indicators: ✓ succeeded, ▶ running, ⏳ waiting approval, ✗ failed.
  - Expandable detail: args preview (monospace), streaming output, approval buttons.

### 6.2 Tool renderer registry

A simple protocol allows custom per-tool rendering with a sensible default:

```swift
protocol ToolCallRenderer {
    static var toolName: String { get }
    @ViewBuilder func body(call: ToolCallActivity) -> some View
}
```

Built-in renderers:

| Tool | Renderer |
|------|----------|
| `run_bash` | Terminal-style card (reuse `ToolExecutionStreamingCard` internals) |
| `web_search` | Query + result count summary |
| `web_fetch` | URL + status + truncated snippet |
| Default | Tool name + args preview + stdout/stderr block |

Future renderers (Phase 2+):

| Tool | Renderer |
|------|----------|
| `image_describe` | Thumbnail + description |
| `gmail_read` | Subject/from/snippet row |
| `calendar_read` | Event title/time row |

Adding a new tool requires only:
1. Backend: classify risk, emit `ToolCallPlanned` + `ToolExecution*` events.
2. iOS (optional): register a custom `ToolCallRenderer`.

### 6.3 Approval flow composition

Approval controls move inside the `ActivityCard`:

- If `ToolCallActivity.state == .waitingApproval`, show embedded approve/reject buttons.
- Buttons call the existing `ApprovalsStore.approve(actionID)` / `reject(actionID)`.
- After approval, the activity transitions through `.running` → `.succeeded` via events.

The existing `InlineApprovalsSection` is kept as a fallback during transition, then removed once `ActivityCard` approvals are stable.

## 7. Implementation sequence

### Phase A: Backend event emission (prerequisite)

1. Emit `ToolCallPlanned` in `executeTurn()` after `planTurn()`.
2. Wrap `executeInlineReadTool()` with `emitToolExecution*` helpers.
3. Generalize `emitToolExecutionFinished` to accept non-bash results.
4. Change inline READ tool-result messages to `role=internal`.
5. Exclude `role=internal` from snapshot/list APIs.

### Phase B: iOS activity model + reducer

1. Add `TurnActivity` and related types to `Models.swift`.
2. Extend `ThreadEventReducerState` with activity tracking.
3. Add activity-focused event handling in `ThreadEventReducer.apply()`.
4. Introduce `ChatTimelineItem` enum.
5. Update `ChatViewModel` to produce `[ChatTimelineItem]`.

### Phase C: iOS activity UI

1. Build `ActivityCard` view.
2. Build `ToolCallRow` with state indicators.
3. Add default tool output renderer.
4. Add bash-specific renderer (reuse existing terminal card).
5. Wire approval buttons into `ActivityCard`.
6. Update `ChatView` to render timeline items.

### Phase D: Cleanup

1. Remove `InlineApprovalsSection` once `ActivityCard` approvals are stable.
2. Remove `ToolExecutionStreamingCard` as a standalone message type (now rendered inside `ActivityCard`).
3. Remove system-message parsing for bash execution results (replaced by event-driven activity).

## 8. Future extensions

- **Phase 3 (jobs/schedulers)**: `TurnActivity` generalizes to `ActivityScope` with `scope_kind = TURN | JOB | DELEGATION` and `parent_scope_id` for nesting.
- **Phase 4 (memory/skills)**: memory checkpoint and skill proposal events render as lightweight activity items.
- **Cross-thread**: a global activity timeline aggregates `TurnActivity` across threads for a "What is Pincer doing?" dashboard.
