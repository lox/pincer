# Pincer MVP (End-to-End Slice v0.1)

Date: 2026-02-13
Goal: prove a real iOS <-> backend loop with approval-gated execution.

## 1. MVP definition

A user can:

1. Send a message from iOS Chat.
2. Receive assistant response from backend.
3. See a proposed external action in Approvals.
4. Approve the action in iOS.
5. See action transition to executed and recorded in audit.

This validates `propose -> approve -> execute -> audit` end-to-end.

## 2. Minimal backend scope

Required persisted primitives:

- `threads`
- `messages`
- `proposed_actions`
- `idempotency`
- `audit_log`

Required endpoints:

- `POST /v1/chat/threads`
- `POST /v1/chat/threads/{thread_id}/messages`
- `GET /v1/chat/threads/{thread_id}/messages`
- `GET /v1/approvals?status=pending`
- `POST /v1/approvals/{action_id}/approve`
- `GET /v1/audit`

Execution behavior:

- Message post creates:
  - one assistant message
  - one `PENDING` proposed action (`demo_external_notify`)
- Approve endpoint marks action `APPROVED`.
- Background Action Executor processes approved actions through idempotency gate.
- On execute success, action becomes `EXECUTED` and emits audit event.

Auth:

- Single dev bearer token for MVP (`Authorization: Bearer <token>`).

## 3. Minimal iOS scope

Screens:

- `Chat`: thread timeline + composer
- `Approvals`: pending approvals + approve action

Behavior:

- Chat send calls backend.
- Approvals list refreshes and shows pending action.
- Approve transitions action state and reflects result.

## 4. Demo script

1. Launch backend.
2. Launch iOS app and set backend URL/token.
3. Create thread and send message.
4. Observe pending approval in Approvals.
5. Approve action.
6. Verify:
   - action status is `EXECUTED`
   - audit contains proposal, approval, and executed events
   - chat shows corresponding system update

## 5. Acceptance criteria

- iOS can read/write through backend with authenticated requests.
- No action executes unless explicitly approved.
- Action execution is idempotent by key.
- Audit log records the critical transitions.

## 6. Out of scope for this slice

- Real LLM provider integration
- Gmail/Calendar/Web tools
- Pairing flow and device key exchange
- Push notifications
- FaceID confirmation gates
