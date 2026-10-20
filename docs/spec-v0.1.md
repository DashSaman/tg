# TG v0.1 implementation specification

Authorized 2026-10-20: finalize the researched architecture and begin implementation;
real Iran/overseas hosts will be supplied later. Initial target is 300 Mbps aggregate
receiver goodput, future target 10 Gbps aggregate. Neither is certified by local tests.

## Selected architecture
A single peer pair exchanges opaque UDP application packets over two authenticated,
encrypted carriers: QUIC DATAGRAM and a full-duplex HTTPS/2 POST. The intended inner
application is WireGuard; its virtual addresses and overseas egress stay unchanged
when the outer carrier changes. TG keeps the same exit-side UDP socket across carrier
reconnects. This version forwards UDP packets and does not modify routes, firewall,
sysctls, WireGuard interfaces, or remote services.

The original work is the session layer, bounded fragmentation/reassembly, health
state, queue-pressure fallback, duplicate suppression and reproducible fault tests.
This is not a claim to invent multipath or camouflage. Native QUIC ALPN is recognizable;
HTTPS/2 is a real web transport but is not proof of DPI resistance. Existing secured
underlays (for example a separately configured Hysteria UDP pipe) may carry QUIC.
REALITY/XHTTP and raw/spoof integrations are later adapters, not simulated features.

## Wire and trust contracts
- TLS >=1.3. QUIC requires CA-verified client cert plus expected DNS identity; all
  clients verify server name and trust roots. HTTPS/2 uses normal server TLS and a
  random 256-bit bearer token inside HTTPS; unknown requests receive ordinary web
  responses. No insecure verification switch or plaintext fallback.
- Configured 16-byte hex session ID is a routing identifier, not a credential.
  Each process has a random epoch. Multiple carriers share a peer epoch; a changed
  peer epoch is accepted only when old links are gone. Target socket persists.
- Data frames: version(1), kind(1), sequence(8), fragment index(2), count(2), total
  length(2), payload. Network byte order. Chunk size 1024, packet limit 8192 bytes,
  default 2048. Probe/Pong use the sequence as challenge and carry no payload.
- Reassembly: max 128 unfinished packets, 2 second expiry; exact shape validation,
  no allocate-before-check, and a 4096-packet duplicate window across carriers.
- HTTPS stream framing uses a 2-byte length prefix, strictly bounded to 1040 bytes.
  QUIC ALPN `tg-dgram-v2` bundles these length-prefixed frames (at most 16) into
  DATAGRAMs of at most 1400 bytes. `SendDatagram`'s current path limit can split
  an unsent bundle without replaying an already accepted bundle. The limit is
  reconsidered once per second as native QUIC PMTU discovery progresses.
  The TG frame body stays version 1. No custom cryptography. Both peers must
  upgrade together: the earlier `tg-dgram-v1` carrier is incompatible.

## Health and resource bounds
Per-carrier control queue is separate from bounded data queue. Pending/unprobed
links cannot carry application data. Probes measure authenticated round-trip on
the carrier, not full Internet exit reachability; metrics name that scope explicitly.
Failure after probe timeout permits immediate failover; healthy-path replacement
requires repeated advantage and hold-down. Full queues try another healthy link
without partial packet enqueue; successful fallback commits the selected path and
restarts hold-down, rather than switching back for the very next packet.
Completed UDP packets enter one 256-packet output queue and one writer drains up
to 32 immediately available packets. Linux uses `sendmmsg`; other systems retain
individual UDP writes. No batching delay is added. Completed buffers retain their
original reassembler owner until synchronous write consumption or cancellation.
The output queue retains at most 2 MiB of payload storage, plus at most 256 KiB
in the current write batch, independently of the partial-assembly and free-cache
bounds. No retransmission of application UDP inside TG.
No automatic remote service restart. Bounded reconnect backoff never disables probes
on surviving links. Queueing encrypted WireGuard packets does not expose user flow
classes; application-aware traffic scheduling and FEC are future work.

Initial/failure selection score includes smoothed carrier RTT, twice its EWMA
absolute variation, configured path cost and bounded queue penalty. Replacing a
healthy carrier also requires the margin in measured RTT/variation/queue score;
static cost preference alone cannot repeatedly undo congestion fallback.
Probe timeout must be 3–15 probe
intervals (also capped at 300 seconds), keeping pending challenges bounded to 16.
This RTT-variation estimator is not application-packet jitter measurement.

## Delivery and tests
Ship CLI init/check/run, strict JSON, offline test PKI/config generation, loopback-only
metrics, wire counters, readiness based on healthy carriers, and Linux service examples.
Tests use real TLS, HTTP/2, QUIC and UDP sockets: integrity including multi-fragment
packets, transport switch with unchanged backend socket, wrong identity/token rejection,
epoch restart, reordering/duplicates, bounded queues and cancellation. Run race/vet and
build Linux amd64/arm64. An iperf3 acceptance analyzer must use receiver metrics and
reject incomplete data. Real 30-minute 300 Mbps and 24-hour soak remain pending hosts.

## Additional reference review
GoldWaterTunnel public snapshot 5eb9edf1686fdf5ac2bafdaf5bbe8a23bf805a51:
direct WG and WaterWall/Reality are separate engine choices. Its watchdog checks an
application exit and uses consecutive failures and staged recovery. In the reviewed
watchdog, backoff `sleep; continue` also skips probes despite the comment claiming
they continue; TG separates ongoing health observation from reconnect backoff.
See https://github.com/DashSaman/GoldWaterTunnel/blob/5eb9edf1686fdf5ac2bafdaf5bbe8a23bf805a51/watchdog.sh.
The owner's private t6 was also reviewed locally. Only general engineering lessons
carry forward; no private code, configuration, operational identifiers or source text
are published. No code from either reference is copied.
