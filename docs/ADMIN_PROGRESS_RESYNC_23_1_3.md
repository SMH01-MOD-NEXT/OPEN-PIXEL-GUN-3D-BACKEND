# Admin progress reconciliation — client 23.1.3

## Failure and correction

An admin revision previously continued enforcing its currency/XP values after delivery. Gameplay `update_progress` commands were acknowledged without persisting counters, and stale flat projections could override current raw slots. Inventory could therefore survive while spent currency and chest XP reverted.

Admin edits now establish a durable per-player revision, with per-device acknowledgements. Each authenticated session captures its write revision. Until the current device completes the current download, uploads cannot replace the pending admin state. Old-session writes are rejected. A correlated `sync_full` followed by stock Login completion acknowledges only the served current revision; a second boot/raw echo is not required. After acknowledgement, ordinary gameplay updates can change counters again.

Core scalar values, flat progress, existing client-state counters and raw core slots are reconciled. Ordinary snapshots do not manufacture missing core slots. Admin replacement preserves non-core sections and reconciles valid raw core data. Nickname-only edits also create a revision; saving identical values does not force reconnection.

## Measured gameplay command contracts

- 10: add currency, `p.c` currency name and `p.v` amount.
- 66: spend currency, same fields.
- 64: absolute level, `p.l`.
- 65: absolute experience, `p.e` (not a delta).

Commands are persisted before a success ACK. A receipt keyed by authenticated device, command `ci`, command ID and canonical payload prevents repeated application across retries, repacked batches and store reopen. Invalid values, insufficient balance and persistence errors do not partially apply a batch. Receipt capacity is bounded at 100,000 and fails explicitly instead of evicting replay protection. A new admin revision clears the previous epoch's receipts; old authenticated epochs remain fenced.

This is compatibility persistence, not a complete anti-cheat economy. Pricing and rewards are not comprehensively validated server-side. Unknown inventory/analytics command schemas are not guessed; existing full-slot synchronization remains responsible for those sections.

## Targeted online/offline delivery

1. Persist the edited account and new revision before reporting success.
2. Abort live WebSocket transports belonging to that canonical authenticated player only. Unrelated players are not disconnected. Abnormal transport loss lets stock BestHTTP reconnect; a graceful Engine.IO close would stop it.
3. Return `opg3d_admin_resync: "<player>:<revision>"` in authenticated `progress_status` only while that device has a pending revision.
4. The matching frontend bridge loads the stock AuthorizationScene on the Unity main thread. Stock authorization downloads all progress sections and sends the completion acknowledgement.
5. Offline accounts keep the pending revision in persistent storage and download it on next login, including after backend restart.

The frontend bridge is required for automatic refresh of an already-open 23.1.3 lobby. Backend-only reconnect does not rerun that lobby's boot coroutine. No global resync flag, fake ACK, preference deletion, cache wipe or Android process kill is used. WebSocket live delivery was verified; polling-only sessions have no immediate transport-abort capability and retain the durable next-login synchronization path.

## Verification — 2026-10-19

- Offline revision 22: no active sessions at edit; next login downloaded 87 sections and acknowledged the revision.
- Final online revision 26: edit at 18:28:08 local time, full download at 18:28:21, completion at 18:28:23. The Android PID did not change; no manual restart was needed.
- Subsequent gameplay changed currency and chest XP without a new admin revision. A later snapshot/login retained 997417 coins, 997654 gems and 0 XP instead of replaying the old admin grant. These are an observation, not values to restore.
- Lobby, arsenal and store currency displays were checked during the resync verification. Inventory remained present; no app data was cleared.
- `go test ./...`, race tests for store/API/SIO, `go vet ./...` and backend build passed.
- Regression coverage includes pending/old-epoch writes, cross-player isolation, duplicate retries/reopen/concurrency, atomic failures, fresh profiles, no-op edits, targeted transport ownership and failed completion acknowledgements.

Deployed backend SHA-256: `172246245bfe311f9a297f483dafd6097bf8cf630279c29a8b594f751a1d71b3`.
Companion native library SHA-256: `1badf6acd0a65c2c5e722b7dec2828e1012e584cab9cb906ba069b8292849d02`.

Private backups and diagnostic logs remain outside the repository. Do not restore an earlier profile over subsequent legitimate purchases. The independent intermittent 21% startup case is not claimed fixed by this change.
