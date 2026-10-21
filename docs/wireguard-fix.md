# WireGuard throughput diagnosis and repair

The validated local default is reliable `quic-stream` plus HTTPS/2 backup:
the [completed repaired test](wireguard-fixed-validation.md) received 350.00 Mbps,
minimum ten-second window 303.59 Mbps, with both TG memory gates and three loaded
outage recoveries passing. The profile includes 50 ms bounded packet admission.
The best-effort DATAGRAM experiments below are retained as diagnostic history,
not presented as stable bulk-TCP acceptance. See the [reliable QUIC decision](reliable-quic-decision.md).

The earlier complete 30-minute test received 199.76 Mbps and failed its 300 Mbps
gate. Direct kernel WireGuard bypassing TG received 350.08 Mbps in a short control.
The deficit was in the combined relay pipeline, not evidence that WireGuard itself
has a 200 Mbps limit. The original results remain in [the baseline report](wireguard-validation.md).

## Measured causes

1. Carrier receive called local UDP write synchronously for every completed packet.
   A lab-only native QUIC trace counted 6,540 `buffer_full` DATAGRAM drops at the exit
   in the first 25 seconds of daemon lifetime. Namespace UDP receive-buffer errors
   stayed zero. CPU profiles attributed over half of sampled CPU time to network
   syscalls. Blocking local delivery therefore also delayed draining QUIC's bounded
   128-frame receive queue. A larger TG transmit queue does not enlarge that queue.
2. A normal WireGuard packet at inner MTU 1280 crosses the fixed 1024-byte TG chunk
   boundary. The old QUIC carrier sent each fragment as a separate DATAGRAM. Reducing
   inner MTU to 960 raised a short control to 310.50 Mbps, but was only a diagnostic:
   final validation retains inner MTU 1280. QUIC v2 instead bundles available
   length-prefixed frames subject to the native path-MTU limit.
3. Whole-packet overflow fallback did not commit the alternate path in the selector.
   Adjacent packets could oscillate between carriers while the preferred selector
   remained unchanged. Committing fallback reduced switches, but static QUIC cost
   preference still repeatedly undid it after hold-down. Healthy replacement now
   requires both preference and a measured RTT/variation/queue advantage.
4. Short tests still missed periodic stalls: candidate `a63e8c6` passed 90 seconds
   but an attempted long run later showed a 140.50 Mbps ten-second receiver window.
   That run was deliberately interrupted after about 332 seconds, retained as a
   failed diagnostic, and is **not** counted as a completed 30-minute test. Brief
   transmit-queue bursts must receive bounded backpressure before path replacement.

The repair separates completed-packet delivery into a bounded 256-packet queue and
one writer draining at most 32 already available packets. Linux uses `sendmmsg`;
other platforms retain ordinary writes. No batching timer is added. Buffers retain
their original reassembler owner until consumed or cancelled. A real oversized
second UDP datagram exercised a partial syscall result and caught x/net returning
`-1` on the failing syscall; that error cannot subtract the previously sent prefix.

An intermediate repair waited about 2 ms for preferred queue space, releasing the
engine lock. Later controls below eliminated overflow path spraying altogether;
the final default retains a pending whole packet for up to 50 ms on that same
bounded queue. Control probes and cancellation remain independent.

The 2 ms wait alone was insufficient: a 300-second control still reached only
90.18 Mbps in its slowest ten-second window and exposed ingress UDP buffer drops.
A real 2,048-packet socket burst received only 910 packets with the old 1 MiB
receive-buffer request. A per-socket 4 MiB request passes that burst test without
changing global sysctls. OS limits can still clamp the request.

Increasing receive headroom alone also did not resolve cross-carrier instability:
both carriers averaged 337.52 Mbps with a 141.93 Mbps minimum window over 120s.
In contrast, each carrier alone, on the same candidate, passed a 120s control:
QUIC 350.01 / 339.94 Mbps, HTTPS 350.01 / 318.75 Mbps (average / minimum 10s).
The simultaneous-carrier run recorded 14,159 TCP retransmissions versus 3,299
for isolated QUIC. This isolates the remaining problem to per-packet overflow
steering in the combined policy, rather than an intrinsic per-carrier capacity cap.
TG carries an opaque WireGuard aggregate; it has no inner-flow classification and
no packet resequencing. The repair therefore uses a single active data carrier,
switching on carrier failure by default (healthy improvement needs opt-in), with bounded
whole-packet admission and explicit overload drops instead of packet spraying.

Candidate `12049ea` removed per-packet spraying but its adaptive queue score still
moved the aggregate between a loaded and an idle carrier. Its long attempt was
interrupted around 415 seconds after a 104.85 Mbps minimum window, with zero ingress
UDP receive-buffer drops. The default now sets `auto_upgrade: false`: retain a
healthy active path, and switch when it fails. Recovering a different path does
not force failback. The earlier adaptive mode is available only by explicit opt-in;
its short passes are not presented as long-run acceptance.

A separate scheduler control on the preceding candidate kept all other settings
and set `GOMAXPROCS=2` per TG: 350.01 Mbps average / 349.73 Mbps minimum over 120s.
That was a short scheduler control, not the final accepted profile. The VM still
had 12 logical CPUs; Go worker concurrency is not a 2-vCPU-host capacity claim.

Sticky candidate `098d82a` retained QUIC with zero carrier changes and zero kernel
UDP receive-buffer drops, but an attempted long run was interrupted around 474s
after an interval-only reconstruction reached 260.18 Mbps in a ten-second window
(live read-counter telemetry had observed about 248 Mbps). The native QUIC receive
queue still shares frame-processing scheduling. An independent drainer now moves
native datagrams into a bounded 2,048-slot queue before frame decoding, retaining
at most 2.74 MiB of payload per QUIC connection without another copy.

Controlled profiled QUIC checks at 350 Mbps: a 512-slot drainer with Go concurrency
2 recorded 181 native receive drops; 2,048 slots with the same concurrency recorded
55; the same 2,048-slot binary with concurrency 4 recorded 4. All three short checks
received about 350 Mbps with minimum windows above 349 Mbps. Drop counters cover
only the first 25 seconds including setup; these are not 30-minute acceptance.
The final measured profile uses `GOMAXPROCS=4` per TG on the same 12-logical-CPU VM.

Tests cover a paused frame consumer with a 2,048-datagram burst, per-call deadlines
followed by successful receives, explicit close with full prefetch, and native
connection closure while downstream is paused. The native-close test first exposed
a blocked-reader leak; observing native connection context and cancelling/draining
wire-owned storage fixes it. Accept callback return also closes the wire.

## Controls, including failed intermediate versions

All are local eight-stream TCP controls through kernel WireGuard. Rates are Mbps.
Profile runs include profiling overhead and cover only 30 seconds of load; drop
counts cover the first 25 seconds from daemon start, including setup. These are
diagnostic comparisons, not long-run certifications.

| Variant | Duration | Receiver average | Minimum 10s | Verdict |
|---|---:|---:|---:|---|
| Original QUIC, native drop trace/profile | 30s | 237.42 | 224.49 | fail; 6,540 native receive drops |
| Async/batched UDP output only, QUIC/profile | 30s | 230.92 | 218.06 | fail; 519 native receive drops |
| Output batching plus QUIC frame bundling/profile | 30s | 350.02 | 300.90 | short pass; 481 native receive drops |
| HTTPS with output batching | 30s | 350.08 | 348.69 | short pass |
| Both carriers, before sticky fallback | 120s | 350.01 | 272.55 | fail |
| Sticky fallback, cost still drives upgrades | 90s | 350.03 | 151.93 | fail |
| Measured upgrade required (`a63e8c6`) | 90s | 350.04 | 347.63 | short pass, later long attempt failed |
| Same candidate, manually interrupted long attempt | ~332s | not acceptance | 140.50 | fail; incomplete/error receipt retained |
| Reliable stream, 2 ms admission, interrupted | ~859s | not acceptance | 278.20 | fail; ingress burst drops remain |
| Reliable stream, 50 ms admission (`eef5f85`) | 1800s | 350.00 | 303.59 | full local receiver/memory pass; then three outage recoveries |

The first reliable stream attempt still discarded pending ingress packets after
two milliseconds, even though path changes and kernel UDP errors were then zero.
A real application-loop regression reproduces the loss with a ten-ms carrier
stall. Holding the pending packet up to 50 ms fixes that regression without adding
queue slots. The completed follow-up still recorded sparse kernel ingress losses
and two TG drops; its acceptance is receiver goodput over ten-second windows, not
zero loss or an instantaneous 300 Mbps floor. Twelve full one-second intervals
were below 300 Mbps, minimum 182.52 Mbps. See the complete evidence and scope in
[the final validation](wireguard-fixed-validation.md).

## Compatibility and remaining scope

Both endpoint configurations must upgrade together: default `quic-stream` uses
ALPN `tg-stream-v1`; optional `quic` DATAGRAM uses `tg-dgram-v2`. The modes do not
negotiate with each other or with old carrier v1. TG session frame version 1 and
HTTPS framing are unchanged. Optional DATAGRAM bundles are bounded to 1400 bytes
and 16 frames. Native `SendDatagram` rejection
splits only the unsent bundle; no previously accepted bundle is replayed. The
limit is reconsidered once per second as native PMTU discovery advances.

No cryptography, host routes, firewall, sysctls, WireGuard MTU or credentials are
modified by TG. WireGuard remains configured separately. Local performance says
nothing conclusive about Iran WAN capacity, DPI resistance, 10 Gbps, or 24-hour
memory behavior. A live external receiver-read counter helps detect a failed long
attempt early; final acceptance always uses independent iperf receiver JSON.
