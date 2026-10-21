# TG redundant-carrier design — 2026-10-22

**Historical mirroring prototype; superseded by the
[acknowledged repair design](reliable-redundancy-design.md).** Its short fault
controls failed the stricter one-second target. Statements below describe that
prototype, not the current reliable-redundancy implementation.

The owner requested another measured combination to remove the remaining short
throughput dips and reduce interruption when one carrier fails. The prior complete
kernel-WireGuard run passed ten-second windows, but the same raw receiver fails a
one-second rolling gate (minimum 183.29 Mbps). Do not replace that historical result.

## Selected bounded extension

1. Linux UDP input uses batches of up to 32 messages (`recvmmsg` via x/net). Each
   admitted packet still receives its own immutable payload copy. Datagram bounds,
   IPv4/IPv6 source peer handling, zero/oversized rejection and cancellation remain.
   Read and write descriptors/storage are separate; at most 32 input buffers are kept.
2. Explicit `redundant: true` offers every complete fragmented packet, with the same
   ID, to all healthy reliable carriers. `tg init --redundant` provisions both peers.
   Single-carrier failover remains the default. A successful replica does not wait
   for another full queue; if none can admit the packet, admission waits on any
   eligible queue for at most 50 ms. Queues and packet bounds do not grow.
3. Both paths remain authenticated reliable QUIC-stream and HTTPS/2. The receiver
   reassembles/deduplicates copies before application UDP delivery. A separate
   publication mutex serializes completion/publication without holding the engine
   lock while output is full. This preserves completion order, not global ID order.
4. Redundant receivers retain 262,144 completed IDs (2 MiB), rather than 4096. A
   deterministic regression delays a unique packet until 4096 newer packets finish:
   the legacy replay window discarded it. Payload-cache and partial-assembly limits
   remain unchanged. The larger ID history is still finite, not a time guarantee.

## Alternatives and limits

- Input batching alone addresses kernel burst pressure, but cannot cover an outage
  during the previous four-second health-detection interval.
- FEC uses extra bandwidth to repair a bounded amount of packet loss. It does not
  by itself deliver traffic across a fully unavailable path. Custom FEC/ARQ and new
  cryptography are not added in this extension.
- Native MPTCP supplies connection sequencing and reinjection across TCP subflows,
  but is not a drop-in combination of the existing UDP QUIC and HTTPS carriers.
- Concurrent full copies can approximately double carrier bandwidth. Independent
  carrier protocols on one IP still share IP blocking and bottleneck risk. Each
  surviving path needs enough capacity for the requested aggregate throughput.

Focused review identified a retained limit: if one queue is full, a packet can
have only one admitted copy. Failure of that other carrier before transmission
can still lose it. There is no session acknowledgement/replay cache. Therefore
this is bounded best-effort redundancy, not a universal lossless handover promise.
Inner TCP can recover residual loss; test receiver goodput and interruption rather
than declaring success from carrier counts. Copy-skipping is exposed in status.

The default wire formats and ALPNs remain unchanged. Existing receiver deduplication
can accept mirrored frames, but both endpoints must opt in to protect both traffic
directions. Redundant mode permits only reliable carriers and at least two configured
carriers; temporary operation with one healthy carrier is expected.

## Verification plan

- Reproduce old one-second receiver failure on the retained complete raw receipt.
- Regression tests for whole-packet mirrored admission, full alternate queue,
  identical packet IDs, concurrent duplicate suppression and delayed unique copies.
- Real IPv4/IPv6 input tests retain earlier payloads through multiple batch refills
  and verify invalid packets cannot replace the last valid application peer.
- Short kernel-WireGuard controls at the same 350 Mbps offered load, eight TCP
  connections, MTU 1280, Go concurrency 4, followed by three single-path cuts.
- Only proceed to a new complete 30-minute test after short controls pass. Require
  average and complete one-second rolling windows >=300 Mbps; explicitly require
  the speed gate during single-path outages, not merely recovery at the end.
- Separately test simultaneous path cuts: report the interruption and recovery;
  physical delivery while all paths are blocked is impossible.
- Retain failed controls, TG RSS/PSS, namespace socket losses, copy/skip counters,
  immutable runtime identity, independent receiver receipts and cleanup records.

## Primary sources reviewed

- [RFC 9000, QUIC streams and flow control](https://www.rfc-editor.org/rfc/rfc9000.html)
- [RFC 9221, unreliable DATAGRAM behavior](https://www.rfc-editor.org/rfc/rfc9221.html)
- [RFC 8684, MPTCP sequencing and reinjection](https://www.rfc-editor.org/rfc/rfc8684.html)
- [x/net PacketConn.ReadBatch](https://pkg.go.dev/golang.org/x/net/ipv4#PacketConn.ReadBatch)
- [UDPspeeder FEC and bandwidth tradeoff](https://github.com/wangyu-/UDPspeeder)
- [Hysteria bandwidth/loss-compensation configuration](https://v2.hysteria.network/docs/advanced/Full-Client-Config/)

The combination and the admission/publication changes are TG code; no third-party
tunnel implementation is copied. These sources motivate mechanisms, not Iran-path
or 10 Gbps performance claims.
