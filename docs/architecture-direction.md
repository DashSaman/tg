# TG architecture decision, 2026-10-20

The owner authorized finalizing the researched design and starting implementation.
The [v0.1 specification](spec-v0.1.md) supersedes the research-only proposal.

One opaque UDP application session uses mTLS QUIC DATAGRAM and bearer-authenticated
HTTPS/2. The inner WireGuard application and exit UDP socket remain independent of
carrier selection. Bounded queues, fragmentation, duplicate suppression, continuing
probes and hysteresis form the original session layer. RTT variation penalizes
unstable paths. Target: 300 Mbps aggregate receiver goodput, then 10 Gbps aggregate.

Local correctness tests do not certify speed or Iranian DPI resilience. Application
exit checks, flow classes, FEC, bonding, REALITY/XHTTP and capability-gated spoof
adapters remain future work. Health explicitly means authenticated carrier round
trip. A timeout does not diagnose censorship.

See [research](research/tunnel-research-fa.html), [sources](research/sources.json),
and [validation](validation.md). Psiphon tactics, sing-box URLTest, XHTTP direction
separation, Spoof Tunnel asymmetry, Glorytun/MPTCP bonding and TunnelGuard hysteresis
are prior art, not TG inventions. GoldWaterTunnel and private t6 were additionally
reviewed; private implementation is excluded.
