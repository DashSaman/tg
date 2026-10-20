# TG design direction — research stage, 2026-10-20

Owner requirements: a personal Iran-to-overseas tunnel resilient to changing
network conditions and censorship. Start with 300 Mbps aggregate goodput; future
scale target is 10 Gbps aggregate, not one connection. Test hosts will be supplied
later. The owner explicitly requested broad research before implementation and
included IP source spoofing among the options.

Read [the comparative report](research/tunnel-research-fa.html) and its
[source manifest](research/sources.json). These are documentation and selected
static source reviews, not an exhaustive source audit or a runtime benchmark.

The earlier generic pooled reverse-TLS proposal is superseded. A generic TLS
transport alone does not satisfy the stated censorship-resilience requirement.
No tunnel implementation or performance verification is being shipped at this stage.

Proposed direction, subject to experimental validation:

- Keep an authenticated inner session and stable overseas egress independent of
  the outer carrier; preserve virtual addresses through short carrier changes.
- Compare two heterogeneous carriers first, selected from native UDP/WireGuard or
  AmneziaWG, QUIC-based transports, and a web-compatible transport.
- Evaluate IP-spoofing carriers as a separate capability-gated experiment. Both
  directions and the inner security layer must work; a raw carrier is not encryption.
- Select by authenticated payload delivery, loaded latency, capacity and failure
  domain. Track uncertainty; do not declare censorship from a single timeout.
- Separate interactive and bulk scheduling; bound queues, probes, padding and FEC.
- Test against existing adaptive/multipath baselines before claiming original value.

Known prior art includes Psiphon tactics, sing-box URLTest, XHTTP directional
separation, Spoof Tunnel asymmetry, Glorytun/MPTCP bonding and TunnelGuard hysteresis.
These capabilities must not be presented as inventions of TG.

Next design decision: after the research is reviewed, define the first two carrier
interfaces, inner-session continuity contract and reproducible impairment testbed.
Real-host acceptance remains pending the owner's test servers.
