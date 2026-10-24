# Reliable redundant TG session — 2026-10-22

The current reliable-carrier candidate is described in
[shared sender log](shared-sender-log.md). The packet repair design below remains
the experimental DATAGRAM backend and records the development controls.

**Development evidence, not final acceptance.** The first clean redundant build
`b2683aa` later failed during an attempted soak, interrupted at 207s: minimum full
one-second receiver goodput 174.69 Mbps. Its earlier 90s pass was insufficient.
The implementation has since evolved; a fresh complete soak is still required.

## Problem established by measurements

Mirroring alone passed a 60-second steady one-second gate (minimum 344.95 Mbps),
but loaded single-path cuts reached 240.95 Mbps. Increasing per-carrier queues to
4096 reached 277.00 Mbps, still failing. A bounded reorder prototype also failed:
228.91 Mbps; its receiver recorded missing IDs, reached its 4097-packet reorder
peak and discarded 2900 packets. The copies that only entered a subsequently
failed carrier were not available for reinjection. All controls remain diagnostic,
not new 30-minute acceptance. The earlier ten-second-window pass stays historical.

## Selected session repair

Keep authenticated QUIC-stream and HTTPS/2 carriers, immutable whole-packet copies,
batched UDP input/output and stable exit socket. In explicit redundant mode:

- Retain at most **8192 unacknowledged packets** per sender. Reject excess local
  input **before allocating a packet ID**, so admission overload creates no sequence
  hole in the accepted stream. The exact-size packet remains immutable until ACK;
  existing carrier queues may retain their own references after cache removal.
- Add ACK kind 4: header ID is the highest contiguous completed packet; its 136-byte
  payload contains the eight-byte highest completed ID and a 1024-bit selective
  bitmap for the following packet IDs. ACK zero is valid before packet 1.
  Reject malformed shapes, highest below contiguous, or ACK beyond generated IDs.
- Receiver advances cumulative completion only over a contiguous ID prefix. A gap
  plus a higher received ID requests repair implicitly; sender resends a bounded
  burst of up to 128 missing packets on available healthy carriers, skipping
  selectively completed packets. The scanned range is fixed to
  `cumulative+1..cumulative+1024`: do not blindly replay the entire later receive
  window. A first ACK
  prototype did so, amplifying congestion to 8.09 million replays / 1190 local
  input drops; its 180s single-path minimum was 108.71 Mbps and failed. This failed
  control is preserved separately from any later acceptance.
  Coalesced ACK notifications wake repair; a two-ms tick also covers lost ACK/tail
  packets. Only the oldest contiguous missing span (at most 128 IDs, ending at
  the first selectively received ID) may get a first repair after five ms. Other
  holes respect a minimum 20-ms data RTO, adapted to carrier RTT/variation and
  actual packet ACK turnaround, capped at one second. Repeated repairs back off
  exponentially. Retransmitted samples do not update the session RTT estimate.
- Coalesce completion notifications to at most one bitmap per millisecond; the
  two-ms repair worker flushes the last pending ACK even when input stops. Keep
  one replaceable ACK slot per carrier, separate from the eight probe/pong slots.
  Piggyback at most one ACK into a data batch so ACK traffic does not starve data
  or health probes. Duplicate data solicits an ACK without UDP redelivery. ACK
  denotes owned, completed reassembly, not successful application/kernel delivery.
- Each carrier has a separate 1024-frame repair queue. Whole-packet admission
  remains atomic; transmit order gives health probes, repair and ordinary data
  their priority, reserving at most one batch slot for ACK. Priority cannot bypass
  bytes already accepted by the native encrypted transport. Ordinary queue default
  remains 1024; a larger queue increased transition latency in the controls.
- Default output delivers each completed unique UDP packet independently. A global
  sequence barrier made one missing encrypted packet delay all inner TCP flows.
  Explicit `ordered_output: true` retains the strict 16384-packet reorder control
  for comparison; it requires redundancy. No timeout silently discards the repaired
  older packet. Cancellation/epoch changes release held owners. Tests record the
  actual output mode on both peers.
- Reserve a sequence ID only after at least one complete copy enters a carrier
  queue. If every queue or the retained window is full, wait up to 50ms with the
  engine lock released; persistent pressure rejects the packet before numbering.
  Previously numbering zero-copy records let later IDs overtake them. Priority
  repair must not perform ordinary first admission; both errors have regressions.

The replay/completion ID history in redundant mode remains 262144 IDs (2 MiB).
At default max packet size 2048, sender retained payload is at most 16 MiB; receiver
held exact-size payload is at most 32 MiB in the optional strict-order control;
default independent output does not allocate that map. Add bounded descriptors, application/
transport queues and native transport memory. This is a resident-memory measurement
target, not a formal bound on all kernel/QUIC allocations. Warmup/soak must establish
that resident memory settles rather than grows with traffic volume.

## Compatibility and lifetime

Redundant repair peers negotiate a distinct QUIC ALPN and matching HTTPS mode header.
Specifically, `tg-repair-v1` and `X-Tg-Mode: repair-v1`; ordinary stream mode keeps
`tg-stream-v1` and no mode header. The carrier-mismatch tests cover both directions.
Both endpoints must upgrade and opt in; reject a mode mismatch before data mode.
Single-carrier legacy framing/behavior remains available with redundancy disabled.
The default native carrier stays reliable QUIC-stream. Experimental redundant
DATAGRAM uses `tg-repair-dgram-v1`; its failed steady controls do not establish a
stable deployment profile.
ACK metadata is protected by existing authenticated transport encryption; no new
cryptography or IP spoofing is introduced.

An actual peer process restart starts a new receive epoch. Clear surviving sender
retention and restart its data numbering when adopting a different nonzero peer
epoch; reject stale-link ACKs. This can lose unacknowledged data across a process
crash and must not be advertised as crash-proof delivery. Ordinary carrier failure
preserves the peer epoch, sender cache and receiver sequence.

If every path is blocked, data cannot be delivered. Bounded sender admission
eventually rejects local UDP; inner TCP and WireGuard can recover after restoration.
Independent protocols sharing one IP still share IP-block and physical bottleneck
risk. Full copies require additional bandwidth; each surviving path must have
adequate capacity. WAN/DPI, future 10 Gbps and 24-hour certification remain separate.

## Implementation / verification sequence

1. RED/GREEN ACK frame shape and cumulative completion with a gap.
2. RED/GREEN retained packet reinjection: alternate full, sole queued carrier fails,
   alternate recovers, receiver ACK releases cache. Bound cache/ID allocation;
   reject impossible/stale ACKs and verify immutable buffer ownership.
3. Coalesced ACK slot and bounded repair worker; test cancellation and epoch reset.
4. Reliable in-order output: newer packet waits for earlier, no timeout discard;
   epoch change/cancellation releases held packets, and bounded batch writes remain.
5. Reject transport mode mismatch; retain wrong-identity/token tests.
6. Fresh race/vet/Python checks, clean stamped build, then short kernel-WireGuard
   steady/single-path-cut gate at >=300 Mbps in every complete one-second interval.
7. Only after short success, freeze the runtime and run 1800s steady +180s repeated
   single-path cuts +180s mixed/all-path cuts. Require strict steady/single-path
   speed, recovery after all-path cuts, stable PIDs/socket, RSS/PSS gates, cleanup.
8. Preserve raw independent receivers, all failed controls, source/binary identity,
   copy/replay/cache/reorder counters; publish exact scope, successful CI and bundle.

This applies the connection-level acknowledgement/reinjection principle described
by [RFC 8684](https://www.rfc-editor.org/rfc/rfc8684.html) to TG's opaque datagram
session. It is a TG protocol design, not a copied MPTCP or third-party tunnel stack.

## Diagnostic progression (local kernel WireGuard only)

All controls used eight paced TCP streams at 350 Mbps aggregate, MTU 1280 and
GOMAXPROCS=4 per TG process on the same 12-logical-CPU Linux VM. Complete receiver
one-second intervals, not sender rates, determine the stricter gate.

| Change | Ordinary queue | Steady min 1s | Single-path min 1s | Verdict |
|---|---:|---:|---:|---|
| Mirror only | 1024 | 344.95 | 240.95 | Failed |
| Mirror only, deeper queue | 4096 | 345.62 | 277.00 | Failed |
| 50ms reorder expiry | 4096 | 345.41 | 228.91 | Failed |
| Initial cumulative repair | 4096 | 345.10 | 108.71 | Failed |
| Bounded 128-ID repair | 4096 | 345.29 | 193.09 | Failed |
| 128-bit selective repair | 4096 | 345.93 | 198.32 | Failed |
| Priority repair | 4096 | 345.43 | 170.99 | Failed |
| Priority repair, shorter ordinary queue | 1024 | 346.01 | 245.33 | Failed |
| 1024-bit selective repair, coalesced ACK | 1024 | 343.44 | 336.98 | Short pass |

The final short control had 30s steady +90s loaded single-path cuts, no complete
one-second interval below 300 Mbps, zero TG packet drops, and zero retained
packets at its end. It is not 30-minute/memory acceptance. Earlier long-run and
failed-control records remain separate. The new complete soak is still required.
See [the later experiment record](redundancy-experiments.md).
