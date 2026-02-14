# Pincer Authentication and Device Pairing

Status: Implementation detail reference  
Date: 2026-02-15

This document explains the current authentication and pairing behavior in detail.

Relationship to other docs:

- `docs/spec.md` remains the canonical security and architecture contract.
- `docs/protocol.md` remains the canonical wire schema and RPC contract.
- This file describes how those contracts are currently implemented in server code.

## 1. Security model summary

Authentication in Phase 1 is device-scoped and token-based:

1. A client requests a short-lived pairing code.
2. A client exchanges that code for an opaque bearer token.
3. Authenticated RPCs require `Authorization: Bearer <pnr_token>`.
4. Tokens are tied to device records and can be revoked per device.

Core properties:

- LLM/model output is never trusted for auth decisions.
- Auth is evaluated in trusted server code.
- Token secrets are never stored in plaintext.
- Network reachability (LAN, VPN, tailnet) is transport only, not an auth credential.

## 2. Token and pairing primitives

## 2.1 Pairing code

- Generated as an 8-digit numeric code.
- Valid for `10 minutes` (`defaultPairingCodeTTL`).
- Stored as a hash (`pairing:<code>` namespace) in `pairing_codes`.
- One-time use (`consumed_at` set on bind).

## 2.2 Opaque bearer token

- Format: `pnr_<token_id>.<secret>`.
- `token_id` indexes lookup in `auth_tokens`.
- Stored value is HMAC-SHA256 hash of full token using server key (`PINCER_TOKEN_HMAC_KEY`).
- Default TTL is `30 days` (`defaultTokenTTL`).
- Sliding renewal window is `7 days` (`defaultTokenRenewWindow`).

## 2.3 Device record

- Every active session is represented by a `devices` row.
- Tokens are bound to a device via `auth_tokens.device_id`.
- Revoking a device invalidates all its tokens.

## 3. Endpoint auth gates

Middleware behavior (`authMiddleware`) is:

- Public at middleware layer:
  - `AuthService.CreatePairingCode`
  - `AuthService.BindPairingCode`
- All other RPC procedures require bearer token and token validation.

There is no implicit "trusted network" auth mode in current implementation.
Requests received via Tailscale are authenticated the same way as any other request.

Important implementation detail:

- `CreatePairingCode` applies an additional gate:
  - If at least one active device exists, caller must already be authenticated.
  - If no active devices exist, pairing bootstrap is open.

This allows first-device bootstrap while preventing unauthenticated re-pair after enrollment.

## 4. Lifecycle flows

## 4.1 First device bootstrap

1. `CreatePairingCode` (no auth, when active-device count is zero).
2. `BindPairingCode` with code + `device_name`.
3. Server creates:
   - new device row,
   - new token row,
   - `device_paired` audit event.
4. Client stores returned bearer token.

## 4.2 Add/re-pair device after bootstrap

1. Existing authenticated device calls `CreatePairingCode`.
2. New device calls `BindPairingCode`.
3. On successful bind, server:
   - revokes all previously active devices,
   - deletes all existing auth tokens,
   - creates one new active device + token.

Result: pairing to a new device is intentionally single-active-session by default in current implementation.

## 4.3 Token validation on protected RPCs

For each protected request:

1. Parse bearer token, extract `token_id`.
2. Lookup token hash and joined device status.
3. Constant-time compare stored hash vs computed hash.
4. Reject if device revoked.
5. Reject if token expired.
6. Update `last_used_at` periodically.
7. If token is inside renew window, extend expiry by full TTL.

`last_used_at` writes are rate-limited by `lastUsedUpdateInterval` (1 hour) unless renewal is needed.

## 4.4 Token rotation

`AuthService.RotateToken` requires valid bearer token, then:

1. Deletes old token row.
2. Inserts new token row for same device.
3. Returns new opaque token with fresh expiry/renewal timestamps.

Current implementation does not emit a dedicated audit event for rotate.

## 4.5 Device revocation

`DevicesService.RevokeDevice`:

1. Marks target device `revoked_at`.
2. Deletes all tokens for that device.
3. Writes `device_revoked` audit event with reason `user_revoked`.

If revoking the only active device:

- Subsequent protected RPCs with old token return unauthorized.
- Unauthenticated pairing bootstrap becomes available again.

## 5. Error behavior (auth/pairing relevant)

- Unauthorized (`unauthorized`):
  - missing/invalid bearer token on protected RPCs,
  - `CreatePairingCode` called unauthenticated when active devices exist.
- Unauthorized (`invalid pairing code`):
  - code missing, expired, consumed, or unknown at bind.
- Not found:
  - revoke requested for nonexistent device.
- Invalid argument:
  - missing `device_id` for revoke,
  - missing `code` for bind.

## 6. Audit events in scope

Auth/pairing lifecycle currently writes:

- `device_paired`
- `device_revoked`

Other side-effect lifecycle events are documented in `docs/spec.md` and `docs/protocol.md`.

## 7. Operational notes

1. Local/dev commonly runs on plain HTTP.
2. Production/remote access should terminate TLS before traffic reaches clients.
3. For tailnet access, prefer exposing Pincer via Tailscale HTTPS service and keep backend bound to loopback.
4. Tailnet access does not bypass pairing: every client still needs a valid paired bearer token.

## 8. Source of truth and drift checks

When behavior changes, update all three:

1. `docs/spec.md` for invariant/policy-level implications.
2. `docs/protocol.md` for request/response schema changes.
3. `docs/auth.md` for lifecycle/edge-case behavior changes.
