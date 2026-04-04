# OpenClaw Pivot Proposal

Status: Proposed
Date: 2026-04-03

## 1. Recommendation

Pivot this repo toward a native iOS operator app for OpenClaw, not a second autonomous backend.

Recommended target:

1. Build a direct iOS client against the OpenClaw Gateway WebSocket protocol.
2. Reuse the current SwiftUI app shell, event-driven chat UI patterns, and testing discipline where they still fit.
3. Do not keep the current Pincer Go backend as the primary runtime.
4. Only add a thin adapter gateway later if mobile-specific requirements prove that a direct client is insufficient.

Short version: make this project an OpenClaw iOS companion, not an OpenClaw competitor.

## 2. Why this makes sense

### 2.1 What OpenClaw already is

As of 2026-04-03, OpenClaw is already Gateway-first:

- A single long-lived Gateway owns messaging surfaces and serves both clients and nodes.
- Control-plane clients connect to the Gateway over WebSocket.
- The browser Control UI also talks directly to that same Gateway WebSocket.
- OpenClaw's current iOS app is a node/peripheral, not the main operator UI, and is still internal preview.
- The Gateway protocol is schema-driven from TypeBox, exports JSON Schema, and already generates Swift gateway models for the macOS app.

That matters because it means a native Swift operator client is aligned with the upstream architecture instead of fighting it.

### 2.2 What this repo already has

This repo is not empty. It already contains:

- a real SwiftUI app shell,
- a streaming chat timeline,
- approval UI patterns,
- settings/session management patterns,
- generated protocol client infrastructure,
- XCUITest and reducer-style client tests,
- and a clear product bias toward "operator console" rather than "toy chat app".

Those assets are reusable at the UI/domain level even if the current backend contract is not.

### 2.3 What is no longer strategic

If the product goal becomes "interact with OpenClaw", the current Pincer backend becomes mostly redundant:

- planner/runtime,
- approval conveyor,
- jobs/scheduler/heartbeat runtime,
- workspace memory model,
- ConnectRPC API surface,
- pairing/token model,
- SQLite server state.

Keeping all of that while also integrating OpenClaw would create two control planes and two trust models in the same product. That is unnecessary complexity unless there is a very specific need for an adapter layer.

## 3. Current-state assessment of this repo

## 3.1 Reuse directly

Keep and adapt:

- `ios/Pincer` SwiftUI app structure
- chat timeline and event-reducer approach
- approvals presentation patterns
- settings/navigation shell
- test harnesses and UI test structure

## 3.2 Reuse conceptually, but rewrite the boundary

Refactor around new domain models:

- `ios/Pincer/APIClient.swift`
- `ios/Pincer/ChatViewModel.swift`
- thread/session/event reducers
- approval models and formatting

These are tightly coupled to Pincer's protobuf/ConnectRPC contract today, but the UI behavior is still useful.

## 3.3 Likely retire

Retire from the mainline product path:

- `cmd/pincer`
- `internal/server`
- `internal/agent`
- `proto/pincer/protocol/v1`
- Pincer-specific auth, planner, jobs, schedules, and audit runtime

If needed, keep them only as a temporary compatibility branch during migration.

## 4. OpenClaw research summary

The current upstream facts that drive the proposal:

1. Gateway is the single control plane, and clients plus nodes both connect over WebSocket.
2. The first frame is `connect`, then the protocol uses typed request/response/event JSON frames.
3. Device identity and pairing are part of the Gateway connection flow.
4. OpenClaw already has session, chat, node, and approval-related methods on the Gateway protocol.
5. The browser Control UI and WebChat are both direct Gateway clients.
6. OpenClaw exposes an OpenAI-compatible HTTP API, but it is explicitly described as a small compatibility surface and a full operator-access boundary, not a narrow mobile-client contract.

Implication:

- The real product API for an iOS operator app is the Gateway WebSocket protocol.
- The HTTP OpenAI-compatible surface is useful for compatibility experiments, but not the right primary contract for a serious native operator app.

## 5. Architecture options

## 5.1 Option A: Direct iOS client to OpenClaw Gateway

Build a native Swift client that speaks OpenClaw Gateway WebSocket directly.

Use it for:

- pairing and connection management,
- chat history and send,
- session list/switch/delete,
- node list and node actions,
- exec approval surfaces,
- connection health/presence,
- later cron/skills/config surfaces if needed.

Pros:

- aligned with upstream architecture,
- no extra backend to run,
- no duplicate auth model,
- no protocol translation layer,
- fastest path to a real OpenClaw companion app,
- easiest to keep conceptually simple.

Cons:

- requires implementing Gateway WS handshake, pairing, device identity, and event handling in Swift,
- requires tracking OpenClaw protocol versions,
- requires mobile handling for reconnect/gap refresh.

Assessment:

- This should be the default plan.

## 5.2 Option B: Thin adapter gateway

Keep a small backend whose job is only to normalize OpenClaw for the iOS app.

It would:

- connect upstream to OpenClaw Gateway,
- expose a simplified app-facing API,
- optionally cache state,
- optionally manage push fanout,
- optionally hide protocol churn from the app.

This is only justified if one of these becomes a real blocker:

1. mobile push/background wake requires a server-side fanout layer,
2. protocol churn is too high for a direct Swift client,
3. you want one stable app contract across multiple upstream OpenClaw versions,
4. you need multi-gateway aggregation,
5. you need enterprise auth or proxying that OpenClaw does not provide directly.

Pros:

- can preserve more of the current app boundary,
- central place for cache, replay, push, and policy glue,
- can isolate the app from upstream protocol changes.

Cons:

- another service to build and operate,
- duplicated trust/auth concerns,
- higher maintenance burden,
- temptation to rebuild Pincer instead of staying thin.

Assessment:

- Keep as an escape hatch, not the starting point.

## 5.3 Option C: Full Pincer-style gateway/runtime on top of OpenClaw

Continue owning a full backend and use OpenClaw only as another integration target.

Pros:

- maximum local control.

Cons:

- duplicates OpenClaw's role,
- duplicates control plane and operator semantics,
- duplicates pairing/trust/runtime concerns,
- highest cost,
- weakest strategic focus.

Assessment:

- Do not do this.

## 6. Proposed product shape

The new product should be an OpenClaw operator app for iPhone first.

Suggested v1 surfaces:

1. Gateway connection
2. Pair/connect/disconnect
3. Single-primary-chat UX with a session switcher only when non-main sessions exist
4. Chat timeline with streaming updates
5. Send/abort/retry
6. Node list and basic node status
7. Exec approval view
8. Health/presence view
9. Settings for gateway URL, auth token, tailnet/SSH guidance, paired device identity

Suggested v2 surfaces:

1. cron management
2. skills status/install/update flows
3. config view/edit with safeguards
4. richer node invoke flows
5. push-driven wake and status refresh

Explicitly not v1:

- a second planner/runtime,
- reimplementing Pincer jobs/schedules/memory semantics locally,
- inventing a new approval model on top of OpenClaw,
- a broad compatibility layer before proving the direct client path.

## 7. Recommended implementation approach

## 7.1 New client boundary

Replace the current Pincer `APIClient` with an OpenClaw-first client stack:

1. `OpenClawConnectionManager`
2. `OpenClawGatewayClient`
3. `OpenClawEventReducer`
4. UI-facing domain models decoupled from raw Gateway frames

Key rule:

- keep raw Gateway protocol types at the edge,
- convert them into app-owned models before they hit SwiftUI screens.

That prevents the UI from becoming hostage to upstream frame shapes.

## 7.2 Protocol strategy

Do not hand-roll dozens of ad hoc JSON dictionaries.

Instead:

1. vendor or generate Swift protocol models from OpenClaw's JSON Schema,
2. keep a small handwritten transport/client layer,
3. add a narrow domain-mapping layer for app screens.

This matches how OpenClaw already treats its own protocol and reduces drift risk.

## 7.3 Migration path for the current iOS app

Refactor the app in this order:

1. Introduce an app-level service protocol so views stop depending directly on Pincer RPCs.
2. Build an OpenClaw-backed implementation beside the existing Pincer client.
3. Port Chat first.
4. Port Sessions/Settings second.
5. Port approvals/nodes third.
6. Remove the Pincer backend dependency once parity is good enough.

This lets the repo migrate without a flag day rewrite.

## 8. Phased plan

## Phase 0: Feasibility spike

Goal:

- prove a direct Swift client can pair, connect, send chat, and receive events from a real OpenClaw Gateway.

Deliverables:

- minimal WebSocket handshake,
- `connect` + `hello-ok`,
- `health`,
- `chat.history`,
- `chat.send`,
- event stream handling,
- reconnect after disconnect,
- one manual pairing flow.

Exit criteria:

- iPhone simulator can connect to a real gateway and complete one round-trip chat.

## Phase 1: Core operator app

Goal:

- replace the current backend-backed chat shell with an OpenClaw-backed chat/session app.

Deliverables:

- single-primary-chat shell with conditional session switching,
- streaming chat UI,
- send/abort,
- health/presence banner,
- gateway settings,
- pairing UX,
- basic test coverage for frame decoding and reducer behavior.

Exit criteria:

- the app is useful as a daily OpenClaw chat/session companion without any custom gateway.

## Phase 2: Operator controls

Goal:

- add the minimum admin surfaces that matter on mobile.

Deliverables:

- exec approvals,
- node list/status,
- basic node actions where safe,
- better reconnect/gap refresh,
- remote-connection guidance for SSH/tailnet.

Exit criteria:

- the app is a practical remote operator console, not just a chat wrapper.

## Phase 3: Decide on adapter gateway

Only after shipping the direct client, evaluate whether an adapter is needed.

Create one only if real evidence shows:

- push/background needs cannot be met cleanly client-side,
- the protocol churn cost is too high,
- or multi-gateway aggregation becomes a core product requirement.

If built, the adapter should be:

- stateless where possible,
- translation-focused,
- and explicitly not a second agent runtime.

## 9. Main risks

## 9.1 Pairing/auth complexity

OpenClaw's connection trust model is more complex than the current Pincer pairing code. This is manageable, but it is the first real technical spike.

## 9.2 Protocol drift

The Gateway protocol is versioned and schema-driven. A direct client must track protocol updates. Using generated models reduces the risk, but does not remove it.

## 9.3 Background behavior on iOS

Long-lived WebSocket expectations do not map perfectly onto iOS lifecycle constraints. The app should assume reconnects are normal and design refresh behavior accordingly.

## 9.4 Over-reusing the old architecture

The biggest product risk is psychological, not technical: trying to preserve too much of the Pincer backend because it already exists. That would slow the pivot and blur the product.

## 10. Decision

Recommended decision:

1. Reposition the repo as an OpenClaw iOS companion/operator app.
2. Start with a direct Gateway WebSocket client in Swift.
3. Keep the current Go backend out of the target architecture.
4. Revisit a thin adapter gateway only after a direct-client spike proves a real need.

## 11. Source notes

OpenClaw docs consulted on 2026-04-03:

- Gateway Architecture: https://docs.openclaw.ai/concepts/architecture
- TypeBox / protocol codegen: https://docs.openclaw.ai/concepts/typebox
- Control UI: https://docs.openclaw.ai/web/control-ui
- iOS App: https://docs.openclaw.ai/platforms/ios
- Pairing: https://docs.openclaw.ai/channels/pairing
- OpenAI Chat Completions: https://docs.openclaw.ai/gateway/openai-http-api
- WebChat (macOS): https://docs.openclaw.ai/platforms/mac/webchat
