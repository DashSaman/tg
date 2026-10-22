# Shared sender log for reliable redundant carriers

Status: development candidate; receiver-based speed and memory acceptance pending.

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

Replication consumes extra bandwidth; each surviving carrier needs capacity for
the complete offered traffic. These protocols may share one IP and one physical
path. If all usable paths are blocked, delivery stops. Reconnection, bounded
memory and recovery can be tested; uninterrupted delivery through an all-path
blackhole cannot be promised. Local kernel-WireGuard measurements do not certify
Iran/WAN latency, DPI resistance, future 10 Gbps or 24-hour operation.
