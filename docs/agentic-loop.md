# Agentic Loop: Post-Approval Continuation

Status: Proposed
Date: 2026-02-17
References: `docs/spec.md`, `docs/protocol.md`, `docs/activity-model.md`, `PLAN.md`

This document proposes changes to Pincer's turn execution loop so that tool execution results feed back into the LLM for re-planning, enabling multi-step adaptive agent behavior while preserving approval gates.

## 1. Problem

Today's turn execution ends when a non-READ tool is proposed. The flow is:

```
user message → planTurn → split READ/non-READ
  → READ tools execute inline and loop back to planTurn ✓
  → non-READ tools: persist as PENDING proposals, emit TurnCompleted, turn ends ✗
```

After the user approves and the tool executes, the output is persisted as a `system` message but the LLM is never called again. The agent cannot observe results, adapt, or continue.

Consequences:

1. **No adaptive multi-step execution.** Amp runs 6 bash commands, observing each output before deciding the next. Pincer batches all proposals upfront with no feedback loop.
2. **No synthesis.** Amp produces a summary ("All six tests passed ✓"). Pincer's turn is already completed before execution happens.
3. **No error recovery.** If one command fails, the agent cannot adjust. The remaining batch executes blindly or not at all.
4. **Assistant text discarded.** When tool proposals exist, the assistant message is not persisted or streamed. The user sees only "run_bash — Proposed by planning model" with no narrative context.
5. **Static justifications.** Every proposal says "Proposed by planning model." instead of showing the actual command or explaining intent.

## 2. Goals

1. After approved tool execution, feed results back into the LLM and continue the planning loop.
2. Enable multi-step adaptive execution: plan → propose → approve → execute → observe → re-plan.
3. Preserve the `proposed → approved → executed → audited` conveyor — no security regression.
4. Always surface assistant text alongside tool proposals.
5. Keep the change scoped to backend orchestration. iOS already handles the event stream; it should work with minimal changes.

## 3. Non-goals

1. Auto-approval or approval bypass for any tool class. All non-READ tools still require explicit approval.
2. Batch approval ("approve all N"). Useful but orthogonal; can be added independently.
3. Streaming LLM responses. Valuable but a separate concern (requires switching to streaming chat completions API). This proposal works with the current synchronous planner.
4. Subagent/delegation support. Defer to Phase 4.

## 4. Design

### 4.1 Turn state machine

Today a turn has two terminal states: `TurnCompleted` (with assistant message) or `TurnFailed`. This proposal adds a paused state:

```
TurnStarted
  → [planning loop]
  → TurnCompleted              (no tools, or only READ tools resolved)
  → TurnPaused                 (non-READ tools proposed, awaiting approval)
      → [user approves/rejects each action]
      → [all actions resolved]
      → TurnResumed
          → [planning loop continues with tool results in context]
          → TurnCompleted      (LLM produces final answer)
          → TurnPaused         (LLM proposes more tools — cycle repeats)
  → TurnFailed                 (planner error, budget exceeded)
```

A turn may cycle through `Paused → Resumed → Paused` multiple times as the agent works through a multi-step task. Each cycle is bounded by `maxInlineToolSteps` (shared across the entire turn, not per-pause).

### 4.2 Backend changes

#### 4.2.1 `executeTurn` becomes resumable

The current `executeTurn` function runs the planning loop synchronously and returns. The proposed change:

1. When non-READ tools are proposed, persist them as PENDING (unchanged).
2. Persist the assistant message alongside proposals (new — currently discarded).
3. Emit `TurnPaused` instead of `TurnCompleted`.
4. Record the turn's `step` counter and `turnID` so the loop can resume later.

#### 4.2.2 Post-execution continuation trigger

After `executeApprovedAction` completes (or after the last pending action in a turn is resolved), check whether the turn should resume:

```
func (a *App) maybeResumeTurn(ctx context.Context, actionID string) {
    // 1. Look up the turn that owns this action (via source_id → thread_id, turn association).
    // 2. Count remaining PENDING actions for this turn.
    // 3. If all actions are resolved (EXECUTED or REJECTED):
    //    a. Emit TurnResumed event.
    //    b. Call executeTurn in continuation mode.
    //       - Planner sees tool results (already persisted as system messages).
    //       - Loop continues from where it left off.
}
```

This is called from `ApproveAction` after execution succeeds, and from `RejectAction` after rejection.

#### 4.2.3 Tool results as planner-visible messages

Today, `executeApprovedAction` persists tool output as `system` role messages. The planner already loads these via `loadPlannerHistory`. No change needed — the LLM will see the results on the next `planTurn` call.

However, the format should be aligned with the inline READ tool format for consistency:

```
Current:  "Action tc_abc123 executed."  (or bash output blob)
Proposed: "[tool_result:run_bash] <stdout>\n<stderr>\nexit: 0 (1.2s)"
```

This gives the planner structured output it can reason about, consistent with how READ tool results appear.

#### 4.2.4 Turn budget accounting

The `step` counter must persist across pause/resume cycles. A turn that has used 7 of 10 steps before pausing resumes with 3 remaining. This prevents unbounded loops.

Add a `turn_state` table or columns to track:

```sql
ALTER TABLE threads ADD COLUMN active_turn_id TEXT;
ALTER TABLE threads ADD COLUMN active_turn_step INTEGER DEFAULT 0;
```

Alternatively, store in a dedicated `turns` table if we want richer turn metadata (start time, budget, pause count).

#### 4.2.5 Always persist assistant text with proposals

In `finalizeTurn`, when proposals exist, also persist and stream the assistant message:

```go
// Current: assistant message is only persisted when len(proposedCalls) == 0.
// Proposed: always persist it.
assistantMessageID = newID("msg")
result.AssistantMessage = assistantMessage
tx.ExecContext(ctx, `INSERT INTO messages ...`, assistantMessageID, threadID, assistantMessage, nowStr)

// Stream it to iOS
a.appendThreadEvent(ctx, &protocolv1.ThreadEvent{
    Payload: &protocolv1.ThreadEvent_AssistantTextDelta{...},
})
```

### 4.3 Protocol changes

#### 4.3.1 New event types

```proto
message TurnPaused {
  uint32 pending_action_count = 1;
  uint32 steps_used = 2;
  uint32 steps_remaining = 3;
}

message TurnResumed {
  string resumed_reason = 1; // "all_actions_resolved", "partial_continue"
  uint32 steps_remaining = 2;
}
```

#### 4.3.2 Existing events unchanged

`TurnStarted`, `TurnCompleted`, `TurnFailed`, `ProposedActionCreated`, `ProposedActionStatusChanged` all remain as-is. iOS already handles them.

### 4.4 iOS impact

Minimal. The iOS `ThreadEventReducer` already processes event streams incrementally. The new events affect the activity card state:

1. `TurnPaused` → activity card shows "Waiting for approval" instead of "Done".
2. `TurnResumed` → activity card shows spinner / "Continuing…".
3. Additional `AssistantTextDelta` / `ToolCallPlanned` / `TurnCompleted` events arrive as the resumed turn progresses — these are already handled.

The main visible change: after approving all actions, the chat will show the agent continuing to work (thinking, proposing more tools, or producing a final summary) instead of going silent.

### 4.5 Justification and preview improvements

Change `parseToolCallResponse` in `planner.go` to generate meaningful justifications from tool args instead of the static `"Proposed by planning model."`:

```go
// For run_bash: show the command
// For web_fetch: show the URL
// For future tools: show the primary argument
func justificationForAction(tool string, args json.RawMessage) string {
    switch tool {
    case "run_bash":
        var a struct{ Command string `json:"command"` }
        if json.Unmarshal(args, &a) == nil && a.Command != "" {
            return fmt.Sprintf("Run: %s", a.Command)
        }
    case "web_fetch":
        var a struct{ URL string `json:"url"` }
        if json.Unmarshal(args, &a) == nil && a.URL != "" {
            return fmt.Sprintf("Fetch: %s", a.URL)
        }
    }
    return "Proposed by planning model."
}
```

## 5. Reference: picoclaw comparison

[picoclaw](https://github.com/sipeed/picoclaw) implements the same observe→plan→act loop without approval gates:

```go
for iteration < maxIterations {
    response = LLM.Chat(messages)
    if no tool calls → break
    for each tool call → execute → append result to messages
}
```

Key differences from this proposal:

| Aspect | picoclaw | Pincer (proposed) |
|--------|----------|-------------------|
| Security model | Hard restrictions (command blocklist, workspace sandbox) | Explicit approval per non-READ action |
| Loop pause | Never — all tools execute immediately | Pauses at non-READ tools, resumes after approval |
| Tool result feedback | Appended as `tool` role message, next iteration sees it | Same, via `system`/`internal` messages in `loadPlannerHistory` |
| Multi-step | Up to 20 iterations, LLM decides when done | Up to `maxInlineToolSteps` across pause/resume cycles |
| Streaming | None (blocking LLM calls) | Same (synchronous planner), tool output streamed via events |

The core insight from picoclaw: the loop structure is trivial once tool results feed back into messages. Pincer's addition is making the approval gate transparent to the loop — the LLM doesn't know the loop paused; it just sees tool results appear in context.

## 6. Worked example

User prompt: _"Run some print and sleep commands to test streaming output."_

### Today (broken)

```
Step 1: planTurn → LLM proposes 3x run_bash
Step 2: finalizeTurn → 3 PENDING proposals, TurnCompleted
Step 3: user approves all 3
Step 4: all 3 execute, output persisted as system messages
        — agent is done, no summary, no adaptation —
```

### Proposed

```
Step 1: planTurn → LLM says "I'll test streaming with several commands."
        → proposes run_bash: echo "line 1"; sleep 1; echo "line 2"
Step 2: persist assistant text + 1 proposal, emit TurnPaused
Step 3: user approves → executes → output streamed to iOS
Step 4: all actions resolved → maybeResumeTurn → TurnResumed
Step 5: planTurn → LLM sees output, says "That worked. Let me try stderr mixing."
        → proposes run_bash: echo "out" && echo "err" >&2
Step 6: persist + TurnPaused
Step 7: user approves → executes → output streamed
Step 8: maybeResumeTurn → TurnResumed
Step 9: planTurn → LLM says "Both tests passed. Here's a summary: ..."
        → no tool calls → persist assistant message → TurnCompleted
```

The user sees: assistant narrative interleaved with approval cards and streaming tool output, ending with a synthesis — matching Amp's experience.

## 7. Implementation sequence

1. **Always persist assistant text with proposals.** Smallest change, immediate UX improvement. No loop changes needed.
2. **Rich justifications from tool args.** Change `parseToolCallResponse` to generate meaningful preview text.
3. **Add `TurnPaused`/`TurnResumed` proto events.** Wire through `buf` generation.
4. **Persist turn step counter.** Add `active_turn_id`/`active_turn_step` to threads (or a `turns` table).
5. **Implement `maybeResumeTurn`.** Trigger from `ApproveAction`/`RejectAction` handlers after action resolution.
6. **Make `executeTurn` resumable.** Accept a `resumeFromStep` parameter, skip user message insertion on resume.
7. **Align tool result message format.** Use `[tool_result:run_bash]` format for approved action outputs.
8. **iOS: handle `TurnPaused`/`TurnResumed`.** Update activity card states.
9. **End-to-end test.** Extend eval test to verify multi-step continuation after approval.

Steps 1–2 can ship independently as immediate improvements. Steps 3–8 are the core loop change and should land together. Step 9 validates the full flow.

## 8. Risks and mitigations

| Risk | Mitigation |
|------|------------|
| Infinite loop: LLM keeps proposing tools after each resume | `maxInlineToolSteps` is shared across the entire turn including pauses. Budget exhaustion → `TurnFailed`. |
| Approval fatigue: user must approve every single step | Future work: batch approval, session-scoped trust grants, or risk-class auto-approve policies. Not in this proposal. |
| Race condition: user approves while turn is resuming | `maybeResumeTurn` checks PENDING count atomically. Only triggers when count reaches zero. |
| Long-lived turns blocking resources | Turn timeout (wall clock) from Phase 3 spec. For now, `maxInlineToolSteps` is sufficient. |
| Planner context growth | `loadPlannerHistory` already has a `defaultPlannerHistoryLimit`. Tool results are bounded by execution output caps. |
