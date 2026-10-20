# TG

Personal Iran-to-overseas tunnel research and design.

**Status: research stage. There is no tunnel executable or measured speed claim yet.**
The initial target is **300 Mbps aggregate received application traffic**. The
future scale target is **10 Gbps aggregate**, subject to host and path capacity.
Resilience to variable network conditions and censorship is the primary requirement.

- [Full Persian comparative report](docs/research/tunnel-research-fa.html) — save
  the HTML file and open it in a browser. It is self-contained and needs no CDN.
- [Machine-readable comparisons and source provenance](docs/research/sources.json)
- [Proposed design direction and pending validation](docs/architecture-direction.md)
- [فارسی](README-fa.md)

The review covers VPNs, web/QUIC carriers, reverse tunnels, raw/Fake TCP, IP source
spoofing, DNS/bridge fallbacks and multipath/adaptive systems. It distinguishes
documented capabilities from engineering judgments and real-host measurements.

No external installer, source-address test, public-range scan or remote network
modification was executed. The owner will provide test hosts later. Third-party
source code has not been copied or vendored; report links identify the prior art.
