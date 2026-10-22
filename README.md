# TG

Personal Iran-to-overseas tunnel with an original bounded session layer over
authenticated reliable QUIC and full-duplex HTTPS/2 carriers.

**Status: v0.1 development prototype, locally tested. Real Iran-path performance
and censorship resistance are unverified.** Initial acceptance target: **300 Mbps
aggregate receiver goodput**. **10 Gbps aggregate remains a required, unachieved
goal**, with separate profiling and capacity validation still needed.
See [actual capacity probes and remaining work](docs/capacity-10g.md).
Local load results and their limited scope are recorded
in [the performance report](docs/performance.md); real-host acceptance is pending.

**The earlier DATAGRAM baseline failed:** a 30-minute, eight-stream TCP test
through kernel WireGuard and TG received 199.76 Mbps, with a minimum rolling
10-second rate of 74.19 Mbps. TG process memory remained bounded during that run.
See the [WireGuard results and reproduction](docs/wireguard-validation.md).
An earlier repaired default `quic-stream` completed a local 30-minute test:
**350.00 Mbps average / 303.59 Mbps minimum 10s**,
with bounded TG RSS/PSS and three loaded outage recoveries.
[Historical ten-second-window evidence](docs/wireguard-fixed-validation.md). [Diagnosis and failed intermediate controls](docs/wireguard-fix.md) are retained.

An explicit `tg init --redundant` mode combines both reliable carriers using a
shared bounded sender log, per-carrier replay cursors and selective acknowledgements.
Unique completed UDP packets are delivered independently; strict output ordering
is optional. Both endpoints must upgrade and opt in. Earlier short passes were
followed by failed longer controls; the first attempted soak was interrupted at
207s. The current shared-log runtime passed the complete local profile below.
The older 10s-window result does not establish the stricter one-second target.
[Current design and scope](docs/shared-sender-log.md),
[retained failed controls](docs/redundancy-experiments.md).

The [pre-encryption queue-control profile](docs/wg-qos-profile.md) passed a fresh
**30-minute local kernel-WireGuard test**: **350.00 Mbps average /
324.33 Mbps minimum full 1s**, followed by three minutes of
repeated single-path cuts (minimum **340.39 Mbps**). Every full and
weighted one-second window stayed above 300 Mbps in those phases. TG RSS/PSS
remained bounded. All 1500 loaded pings returned: median 1.27ms,
p95 6.93ms. Three all-path outages caused expected delivery gaps and recovered.
[Complete result, exact profile and limits](docs/wireguard-qos-validation.md).
The runtime includes the [reproduced duplex-lock fix](docs/application-duplex-lock.md).
Load condition: 350Mbps aggregate TCP offered rate. The retained unpaced saturation controls failed;
this result does not establish the same floor under unrestricted load.
The [failed prior bulk control](docs/wireguard-c9-control.md) and the
[earlier full GSO/HTTPS paced failure](docs/wireguard-hybrid-control.md) remain visible.

TG forwards one application's opaque UDP packets. WireGuard is the intended inner
IP tunnel, configured separately. TG keeps the exit UDP socket across carrier
changes and probes both carriers. Default selection retains a healthy active path
and fails over when it stops responding. It does not configure WireGuard, routes,
firewall or NAT. Optional `quic` DATAGRAM mode remains available; its short rate
passes did not establish a stable 30-minute TCP result.
See [فارسی](README-fa.md).

## Local lab

Requires Go 1.26+; deployment targets Linux. TG itself needs no root.

```sh
go build -trimpath -o bin/tg ./cmd/tg
./bin/tg init --dir lab --server-name localhost
./bin/tg check --config lab/exit.json
./bin/tg check --config lab/edge.json
# Separate terminals:
./bin/tg run --config lab/exit.json
./bin/tg run --config lab/edge.json
```

Initialization generates private credentials and two loopback configurations; it
refuses an existing directory. Edge accepts UDP on 31820; exit forwards it to UDP
51820, where a UDP echo server or WireGuard must listen for replies. This is not an
open Internet exit. Keep ca.key offline and provision each host only its own
identity, config, CA certificate and web token. Never commit generated credentials.
Linux modes are 0700/0600; protect Windows directories with appropriate ACLs.

Read the [operator guide](docs/operations.md) before configuring two hosts. Health
means carrier round trip, not application Internet access. Native QUIC is
identifiable; HTTPS/2 alone does not prove DPI resistance. Both carriers on one IP
share IP-block risk. TG does not bond capacities. Redundant mode retransmits
unacknowledged datagrams within its bounded session; the ordinary default does not.
FEC, REALITY/XHTTP and spoof adapters remain future work.

## Design and verification

- [Final v0.1 specification and reference decisions](docs/spec-v0.1.md)
- [Implementation plan](docs/implementation-plan.md)
- [Local verification and remaining acceptance](docs/validation.md)
- [Encrypted local load tests, bottlenecks and reproduction](docs/performance.md)
- [Receiver-based benchmark procedure](docs/benchmark.md)
- [Persian comparative report](docs/research/tunnel-research-fa.html)
- [Primary-source provenance](docs/research/sources.json)

The owner's public GoldWaterTunnel and private t6 were reviewed. Private source
and operational details are excluded. Third-party tunnel code is not copied or
vendored; TLS and QUIC use established libraries.

```sh
go test -race -count=1 -timeout 90s ./...
go vet ./...
python3 -m unittest discover -s tools -p 'test_*.py' -v
```
