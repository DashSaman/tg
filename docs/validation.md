# Local validation ledger — 2026-10-20

Scope: Linux Docker on the developer's Windows machine, Go 1.26, quic-go v0.64.1.
The official `golang:1.26-bookworm` image digest used was
`sha256:ee3901c61e423732605a51f1b8157c849ec01194ea0a274213b5202b2c11e7bf`.
No Iranian server, overseas deployment, public-source spoof test, routing change
or production installation was performed.

Verified behavior in tests:

- Real TLS/HTTP2 and QUIC request/reply; wrong server identity, web token and
  client certificate rejected.
- Real UDP application payload including multi-fragment integrity; blackholed
  QUIC falls back to HTTPS without replacing the exit UDP source socket.
- Carrier recovery, repeated outage and edge process epoch restart preserve the
  surviving exit application socket. New epoch resets receive duplicate state.
- Cancellation interrupts a deliberately blocked carrier writer. Bounded queues
  admit entire packets or none. Reassembly handles reordering, duplicates, expiry
  and resource limits. Stale/unprobed paths are excluded; upgrades are debounced.
- Config exposure/limits/unknown fields rejected; generated credential pair checks
  successfully, refuses overwrite and rejects a missing key.
- Receiver acceptance fixture tests cover stalls, sender-only and incomplete
  receipts, nonfinite/negative values, reverse direction selection, UDP loss/jitter.

Commands are reproducible with a local Go 1.26 toolchain:

```sh
go test -race -count=1 -timeout 90s ./...
go test -race -count=5 -timeout 90s ./internal/engine
go test ./internal/session -run '^$' -fuzz FuzzDecode -fuzztime 5s -parallel 2
go vet ./...
python3 -m unittest discover -s tools -p 'test_*.py' -v
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o dist/tg-linux-amd64 ./cmd/tg
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -o dist/tg-linux-arm64 ./cmd/tg
```

The Linux amd64 and Windows amd64 development executables were smoke-run with
`version`; Linux arm64 is cross-compiled, not executed. See the delivery verification
receipt for final command outcomes. The systemd template is not deployed or tested
under an actual service manager. GitHub CI configuration is supplied; local checks
do not imply a GitHub Actions run has completed.

The local performance follow-up now exercises real encrypted carriers with a
receiver-counted load generator. Five-minute HTTPS, two-minute reverse QUIC,
duplex outage recovery and smaller datagrams are recorded separately in
[performance.md](performance.md). These are same-host application UDP measurements;
they do not include WireGuard or establish two-host WAN acceptance. Failed
diagnostic runs are retained in the delivery receipts alongside successful runs.

An optional attempt to obtain iperf3 in the disposable container failed because apt
could not locate the package; no host packages were installed. Thus automated schema
fixtures are verified, but live iperf3 receipt compatibility remains a host-test gate.

**Unverified:** >=300 Mbps aggregate receiver goodput for 30 minutes, 24-hour soak,
loaded application latency/loss/jitter on Iran routes, real DPI/IP blocking behavior,
physical host capacity and future 10Gbps scaling. QUIC DATAGRAM overhead and TCP
fallback head-of-line behavior require profiling. Read [the acceptance procedure](benchmark.md).
