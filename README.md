# TG

Personal Iran-to-overseas tunnel with an original bounded session layer over
authenticated QUIC DATAGRAM and full-duplex HTTPS/2 carriers.

**Status: v0.1 development prototype, locally tested. Real Iran-path performance
and censorship resistance are unverified.** Initial acceptance target: **300 Mbps
aggregate receiver goodput**. Future 10 Gbps aggregate requires separate profiling
and capacity validation. Neither speed is a current measured claim.

TG forwards one application's opaque UDP packets. WireGuard is the intended inner
IP tunnel, configured separately. TG keeps the exit UDP socket across carrier
changes, probes both carriers, avoids flapping with hysteresis, and falls back under
failure or queue pressure. It does not configure WireGuard, routes, firewall or NAT.
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
share IP-block risk. TG does not bond capacities or retransmit application UDP.
FEC, REALITY/XHTTP and spoof adapters remain future work.

## Design and verification

- [Final v0.1 specification and reference decisions](docs/spec-v0.1.md)
- [Implementation plan](docs/implementation-plan.md)
- [Local verification and remaining acceptance](docs/validation.md)
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
