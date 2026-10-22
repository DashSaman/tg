# Shared sender log for reliable redundant carriers

Status: development candidate; receiver-based speed and memory acceptance pending.

## Local application output shaping candidate

`app_pacing_mbps` is an optional local UDP output cap, default **0 (disabled)**,
valid 1..100000 when enabled. It shapes the completed application datagrams
before they enter WireGuard, separately from QUIC/TCP WAN congestion control or
the generator's offered application rate. It is not a capacity-bonding feature.

The writer permits a bounded 256 KiB burst (at most one maximum-sized 32-packet
batch), then waits for the shared byte-rate budget with a reused cancellable
timer. The wait owns only the current bounded batch, holds no application/engine
or carrier lock, and does not advance receiver delivery out of order. Idle time
cannot accumulate unlimited future credit. Cancellation releases the current
reassembly buffers without a fake send/error count. Unit/race tests cover these
contracts; real-kernel speed impact is still being measured.

The development comparison uses **400 Mbps TCP application input, 500 Mbps local
UDP output cap, original 128 KiB TCP writes**, so the unchanged 300 Mbps receiver
floor is tested without relying on a smaller generator write size. The cap must
include WireGuard packet overhead and may limit other profiles if set too low.
It is not a universal WAN tuning recommendation.

The current candidate adds bounded deferred backup copies to the shared log.
Full immediate replication at 400 Mbps still failed: steady minimum 271.14 Mbps,
with 1298 kernel receive drops at edge's application socket; single-path minimum
216.61 Mbps. TG's own admission-drop counter stayed zero, demonstrating why the
kernel socket counters must remain part of diagnosis. Both monitor clocks and
the separate live observer also had a roughly 0.77s extra scheduling gap near
that event; this correlation alone does not assign all of it to TG or the host.

The repaired DATAGRAM and independently queued stream controls reached about
350 Mbps average but retained occasional one-second failures. With the same
binary, the last QUIC-stream control reached 276.95 Mbps minimum in its 300s
steady phase, and 323.25 Mbps during 180s single-carrier cuts. Neither average
speed nor a successful short cut test establishes sustained acceptance.

## Selected candidate

With `redundant: true` and every configured carrier either `quic-stream` or
`https`, one shared sender log replaces separate per-carrier data/repair queues.
The native encrypted reliable transports handle delivery inside a connection;
TG retains unacknowledged datagrams for replay across connection failures.

- Retain at most 8192 immutable, exact-size application packets. At the default
  2048-byte maximum this is at most 16 MiB of retained payload, plus descriptors.
  At least one healthy carrier and available log capacity are required before
  numbering an accepted packet. A bounded admission wait releases the engine
  mutex while blocked; rejection happens before allocating a sequence number.
- Each carrier has its own ascending cursor into the shared log. A new or
  reconnected carrier starts at the cumulative acknowledgement plus one, sends
  the oldest outstanding packet before newer traffic, and skips packets already
  acknowledged or selectively reported as received. One slow carrier cannot
  hold the other carrier's cursor back.
- `replica_delay_ms` is a bounded **0..50 ms** backup-copy grace, default **10 ms**.
  A healthy carrier with lower configured cost (then name for deterministic ties)
  sends immediately. Other carriers wait for each packet's admission age to reach
  the grace, skipping it if its receiver ACK already arrived. If no better
  healthy carrier exists, they send immediately. An undetected primary blackhole
  can delay the backup by at most the configured grace plus scheduling/transport
  delay, instead of the multi-second health timeout. Setting zero retains the
  immediate full-copy control. A bounded, reused timer prevents ingress-rate busy
  polling while waiting, and health/ACK traffic remains prompt.
- Encode complete packets into transmitter-owned batch buffers under the engine
  lock; release it before any native network send. A concurrent ACK may then
  release the retained packet without corrupting bytes in flight. Maximum-size
  packets fit in one bounded batch. Transport failure discards that connection's
  cursor; reconnect starts from the remaining shared log.
- Reliable carriers do not use the experimental session retransmission timer or
  priority repair queue. This avoids injecting later packet IDs ahead of older
  data and avoids redundant retransmissions inside an already reliable stream.
  Health controls and at most one coalesced ACK still precede each data batch.
- Keep the existing authenticated frame/ACK format and peer epoch checks. ACK
  means completed receiver reassembly, not application or kernel delivery. A
  different peer process epoch resets retention and numbering; it may lose data.
- Receiver output delivers unique completed UDP packets independently. It has no
  global missing-ID barrier across all inner flows. `ordered_output: true` is an
  explicit strict-order comparison mode, with a separate bounded reorder map.

The `quic` DATAGRAM option still selects the earlier selective-ACK packet repair
backend and remains experimental. It is not the recommended reliable profile.
Both peers must use the same intended configuration and pass the setup checks.

## Evidence and limits

A regression first failed because a new carrier sent packet 2 while packet 1 was
stranded on the failed original carrier. The shared-log transmitter sends 1 then
2. Its test acknowledges synchronously during native Send and checks that Send
runs without the engine mutex. Other tests check bounded admission and shutdown.
Fresh race tests passed before the initial performance candidate was built.

Deferred copies reduce duplicate bandwidth when the primary's ACK returns within
the grace. They can add up to that grace to backup delivery after an undetected
primary failure. On a WAN with ACK turnaround longer than the grace, both copies
may still be sent; this local setting is not a universal WAN tuning claim.
Replication consumes extra bandwidth; each surviving carrier needs capacity for
the complete offered traffic. These protocols may share one IP and one physical
path. If all usable paths are blocked, delivery stops. Reconnection, bounded
memory and recovery can be tested; uninterrupted delivery through an all-path
blackhole cannot be promised. Local kernel-WireGuard measurements do not certify
Iran/WAN latency, DPI resistance, future 10 Gbps or 24-hour operation.

The retained packet count and QUIC flow-control windows also bound the bandwidth
delay product. For example, 8192 outstanding 1280-byte WireGuard packets hold
about 10 MiB, representing about 280ms at 300 Mbps before additional overhead.
A higher-latency path can hit that window and needs separate window/capacity
engineering. The present local profile is not a claim of 300 Mbps at any RTT.
