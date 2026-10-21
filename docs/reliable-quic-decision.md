# Reliable QUIC primary for the WireGuard TCP aggregate

The original best-effort DATAGRAM path amplified loss into inner TCP congestion
recovery. Batching local UDP delivery and QUIC frames, larger bounded receive
headroom, independent native receive draining and stable path selection improved
short results. They did not establish the requested long-run floor: candidate
`3b32507` was interrupted around 701 seconds after an interval-only reconstruction
showed a 207.92 Mbps ten-second window. It was not a completed 30-minute result.

The default UDP carrier is now `quic-stream`, using a mutually authenticated
full-duplex QUIC stream with ALPN `tg-stream-v1`. It reuses the existing bounded
two-byte frame lengths and batch writer. TG session framing, epochs, replay window,
application socket ownership and HTTPS/2 backup remain the same. No new
cryptography or packet-level ARQ is implemented in TG. Native QUIC provides byte
ordering, retransmission and flow control. The handshake's absolute three-second
deadline is cleared before data mode; each write refreshes the configured deadline.

The first reliable-stream long attempt (`1e2aa19`) still exposed a separate
ingress admission defect. Around receiver seconds 730–740, the live floor fell to
275.87 Mbps; the preserved interrupted server receipt reconstructs a 278.20 Mbps
minimum ten-second window over 859 seconds. Carrier changes and kernel UDP drops
remained zero, but TG's ingress dropped pending packets when queue space took
more than two milliseconds to return. Reliability inside the carrier cannot
repair a packet discarded before admission. The pending whole packet now waits
up to 50 ms on the same bounded queue, without holding the engine lock. This adds
no queue slots; cancellation and closed-carrier context still interrupt the wait.
A regression through the real application read loop first failed with a ten-ms
carrier stall and now passes. Prolonged overload can still drop packets; this is
not lossless failover or unbounded buffering. The final 30-minute run must validate
this combined repair separately.

The stream receive-window ceilings are 8 MiB per stream and 16 MiB per connection,
with only one incoming bidirectional stream. Local packet reassembly/output limits
remain bounded separately. A paused receiver therefore applies stream backpressure
instead of relying on the small best-effort DATAGRAM receive queue. Partial stream
failure closes that carrier; inner TCP and carrier failover still handle packets
not delivered before closure.

This adds stream head-of-line delay under network loss, as the HTTPS backup already
does at TCP level. Actual WAN throughput, latency and jitter still need the later
Iran/overseas server tests. Optional `quic` DATAGRAM mode remains available with its
explicit bounds and failed long-run controls, rather than being presented as a
stable bulk-TCP mode.

A 60-second kernel-WireGuard control of the new default received 350.079 Mbps,
minimum ten-second window 349.798 Mbps, with zero TG overload drops at both ends.
This is setup validation only; the full 30-minute receiver and memory gates must
be recorded separately before claiming acceptance.
