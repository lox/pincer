# Pincer iOS UI/UX Plan (v0.1)

Status: Planning baseline
Date: 2026-02-13
Scope: Phase 1 control plane for a single-owner backend

## 1. Product posture

The iOS app is an operator console, not a chat toy.

- Primary goal: safe autonomy with clear operator control.
- Every external side effect is visible, reviewable, and explicitly approved.
- The app should make "what happened, what is pending, what needs action" obvious in under 5 seconds.

## 2. Information architecture

Bottom tab bar with five tabs:

1. Chat
2. Approvals
3. Work
4. Schedules
5. Settings

Global UI behaviors:

- Persistent pending-approval badge on the `Approvals` tab.
- Global status banner for backend connectivity/auth issues.
- Pull-to-refresh on all list screens.

## 3. Core object model in UI

Primary objects surfaced in the app:

- Thread
- Message (`user`, `assistant`, `system`)
- ProposedAction (`PENDING`, `APPROVED`, `REJECTED`, `EXECUTED`, with `expires_at`)
- Job (`RUNNING`, `WAITING_APPROVAL`, `COMPLETED`, `FAILED`, `PAUSED_BUDGET`)
- Schedule
- Artifact

## 4. Screen plans

## 4.1 Chat

Purpose: conversational planning and visibility into agent behavior.

Content:

- Timeline of user/assistant/system messages.
- Inline system events for job progress and action proposals.
- Inline approval cards for actions created from this thread.
- Composer with send action.

Interaction rules:

- Message send triggers immediate optimistic insert + streaming assistant response.
- Proposed external actions render as "pending approval" until resolved.
- Approval card tap opens Approval Detail.

Empty state:

- "Ask the agent to research or plan. External actions require approval."

## 4.2 Approvals

Purpose: central queue for decision-making.

List behavior:

- Default sort: highest risk first, then soonest expiry.
- Card fields: tool name, target entity, source, risk, expiry countdown.
- Sticky filter chips: `All`, `High Risk`, `Expiring Soon`, `From Jobs`.

Approval detail:

- Deterministic backend-rendered summary.
- Diff/preview block (email draft body, recipient, calendar change, domain target).
- Actions: `Approve` and `Reject`.

Expiry behavior:

- Expired items move to rejected state with reason `expired`.
- Detail shows immutable audit timestamps.

## 4.3 Work

Purpose: monitor autonomous progress.

List behavior:

- Segmented control: `Running`, `Waiting`, `Completed`, `Failed`.
- Each row shows goal, last update time, and state chip.

Job detail:

- Event timeline (system messages + job events).
- Artifacts list with preview.
- Controls: `Cancel` (phase 1), no pause/resume control unless backend supports it.

## 4.4 Schedules

Purpose: manage recurring and one-shot follow-up loops.

List behavior:

- Schedule row: name/summary, trigger, next run, enabled state.
- Quick actions: enable/disable, run now.

Create/edit flow:

- Trigger types: `cron`, `interval`, `one-shot`.
- Timezone is explicit and visible.
- Confirmation screen shows next two expected runs in local time.

## 4.5 Settings

Purpose: trust and operational configuration.

Sections:

- Pairing status and device identity.
- Connected accounts (user mailbox, bot mailbox, calendar scopes).
- Policy summary (read-only in phase 1).
- Audit access entry point.
- Sign out / revoke device.

## 5. Safety-first interaction patterns

- Any external side effect is represented as a ProposedAction object before execution.
- Approvals are explicit and auditable.
- UI language must distinguish "proposed" vs "executed".
- System messages should explain why an action is blocked (policy, budget, expiry).

Copy pattern examples:

- Proposed: "Draft prepared. Awaiting approval before sending."
- Executed: "Email sent after approval."
- Blocked: "Action blocked: untrusted content in same turn."

## 6. Notifications and interruption design

Phase 1 notifications:

- Pending approval created.
- Approval expiring soon (for example, under 1 hour).
- Job completed/failed.
- Proactive bot reach-out (updates, questions, or check-ins), when policy allows it.

Notification tap targets:

- Approval notifications open Approval Detail.
- Job notifications open Job Detail.
- Proactive reach-out notifications open the related thread or work item.

Proactive reach-out policy (phase 1):

- The model may request a reach-out, but backend policy decides whether a push is sent.
- Valid triggers: owner decision needed, clarification needed, important progress/finding update, repeated failure requiring owner action.
- Operator intervention is one valid trigger, not the only valid trigger.
- Rate limit: at most one proactive reach-out per thread/job per 30 minutes (with priority bypass for urgent safety/approval events).
- Push payload includes only opaque ids and event type; sensitive details are fetched in-app after auth.

## 7. Accessibility and usability requirements

- Dynamic Type support for all text.
- VoiceOver labels for all approval controls and risk indicators.
- 44pt minimum touch targets.
- Color is never the only risk indicator (use icon + text chip).
- Haptics only for high-salience events (approval success/rejection).

## 8. Phase 1 implementation slices

Slice A: shell and nav

- Tab bar, auth/session handling, placeholder screens.

Slice B: approvals vertical

- Approvals list + detail + approve/reject controls.

Slice C: chat vertical

- Thread timeline + composer + inline approval cards.

Slice D: work and schedules

- Job list/detail and schedules list/basic controls.

Slice E: settings and trust surfaces

- Pairing status, account scopes, policy summary, audit entry point.

## 9. Out of scope for this UI plan

- Multi-user switching.
- Rich email client behavior.
- Advanced schedule simulation.
- In-app policy editing for high-risk controls.
- Embedded code execution environments.
