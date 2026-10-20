# Local load validation

This test measures real encrypted UDP application delivery over TG on one Linux
Docker host. It is not a codec benchmark. It includes both TG endpoints, generator,
receiver and optional fault proxies in one Go process. It excludes WireGuard, WAN,
Iranian ISP conditions and DPI. Neither CPU nor heap observations describe a single
deployed TG daemon in isolation. Local success does not certify Internet performance.

## Method

The opt-in `TestLocalLoad` uses fresh temporary credentials and loopback sockets.
Normal `go test ./...` skips it. Each run has two seconds of warmup, a specified
measurement interval, and a 300ms drain period. Sender sequence numbers, payload
checksums and a bounded receiver bitmap detect missing, corrupt and duplicate data.
Receiver bytes arriving during the measurement interval determine goodput; sender
offered rate, TG queue counters and late arrivals cannot inflate that figure.

The default offered load is 350 Mbps total, with 1400-byte application UDP payloads.
In duplex mode it is split equally across directions. Sender pacing is bounded to
128 packets per tick. Rate tests require >=300 Mbps aggregate receiver average and
every complete rolling 10-second window >=300 Mbps. Unimpaired runs also require
<=1% measured receiver loss. All tests reject corruption, duplicates and application
source-socket changes. Latency uses one monotonic process clock and is reported in
0.1ms buckets; it is not a synchronized two-host latency measurement.

Fault cycles keep sending throughout: UDP blackhole at 5s, restore at 11s, HTTPS
disconnect at 16s, restore at 21s, both carriers unavailable at 25s, restore at 28s.
The whole-run average and packet loss include the intentional outage. The normal
whole-run speed/loss gate is not applied to this outage case. Instead it must finish
with healthy carriers and >=300 Mbps in each of the last five seconds. Raw 100ms
receiver bins and health snapshots expose the interruption; it is not removed from
the report. Expected all-path downtime must not be described as lossless failover.

The loss experiment uses a fixed-seed UDP proxy with 1% outer datagram loss, including
control traffic. With fragmented inner packets, application loss can exceed 1%; its
ordinary <=1% loss gate is disabled, while receiver-speed and integrity gates remain.
Different packet sizes are reported separately. No host firewall, route, qdisc or
sysctl is changed. A socket receive-buffer request may be clamped by the OS.

## Changes grounded in the measurements

- HTTPS used separate tiny writes for each frame's length and body, followed by
  frequent flushes. It now combines already available frames into one write/flush,
  at most 16 frames. It adds no batching timer; idle/low-rate control is sent
  immediately. Control queue priority and bounded data admission remain in effect.
- Encoding, frame descriptors, path selection and HTTP reads reuse bounded scratch
  storage. Asynchronous UDP input still gets an owned exact-size copy; it never
  aliases the next socket read. QUIC SendDatagram copies its input before returning.
- Completed reassembly buffers are returned only after the local UDP write. The
  cache holds at most 128 buffers of 8192 bytes. Partial assemblies are separately
  bounded to 128; old incomplete packets are evicted to admit fresh traffic.
  An arrival-ordered list avoids scanning the entire pending table for every frame.
- The application socket requests a 1MiB receive buffer to absorb short scheduling
  bursts. The deterministic Linux regression test lost a 512-packet burst after 92
  packets with the kernel default and passes with the request when host limits allow
  it. No global kernel tuning is performed.
- The default frame queue increases from 256 to 1024. At 350 Mbps with 1400-byte
  packets split into two frames, this covers about 16ms of producer bursts rather
  than 4ms. A controlled 60s reverse QUIC comparison reduced receiver loss from
  1.151% to 0.035%, with p95 latency 1.3ms (previously 1.5ms). This is a bounded
  headroom tradeoff: a full queue retains application payloads longer, and slower
  WAN paths still need independent latency testing. Existing explicit queue values
  in configuration files are preserved; regenerate or edit them to use the new default.

The allocation comparison is total allocated bytes over comparable short runs,
not resident memory and not a claim that gigabytes were held in RAM. Heap snapshots
include test-harness arrays and retained health samples. CPU and allocation profiles
are delivered alongside the receipts so third-party QUIC costs remain distinguishable
from TG's own allocations.

## Reproduce

Use Go 1.26+ on Linux. Run performance cases sequentially, without race instrumentation
or unrelated CPU-heavy jobs. Record host capacity, source revision, payload size,
direction and duration. The delivery receipts also retain a digest of Go sources.

```sh
mkdir -p receipts
go test ./internal/engine -run '^TestLocalLoad$' -count=1 -v -timeout 150s \
  -cpuprofile "$(pwd)/receipts/quic.cpu" \
  -memprofile "$(pwd)/receipts/quic.mem" -o "$(pwd)/receipts/engine.test" \
  -args -tg.load -tg.duration 120s -tg.carrier quic -tg.mbps 350 \
  -tg.direction forward -tg.packet 1400 -tg.min-mbps 300 \
  -tg.report receipts/quic.json
```

Use `-tg.carrier https -tg.duration 300s` with a timeout of at least 330s for the
long HTTPS run. Use `-tg.carrier both -tg.direction duplex -tg.fault cycle
-tg.duration 90s` for faults, and `-tg.carrier quic -tg.loss 1` for outer UDP loss.
Add `-race` and reduce rate/target for separate correctness testing; never compare
race-instrumented speed with the performance receipts. Profile with
`go tool pprof -top receipts/quic.cpu` or `-alloc_space` on the memory profile.

Full 30-minute and 24-hour acceptance, production WireGuard overhead, real Iran
path quality and future 10Gbps scaling remain separate unverified milestones.

## Recorded local results — 2026-10-20

Same Linux Docker VM, 12 logical CPUs, about 20 GiB VM RAM, Go 1.26.10; both endpoints plus generator/receiver in one process; no WireGuard, WAN or DPI. Performance cases offer 350 Mbps aggregate, duplex split equally; the separate race case offers 30 Mbps. Heap includes receipt tracking. Total allocation is cumulative churn, not resident RAM. Fault averages include outage.

| Case | Seconds | Payload | Aggregate receiver Mbps | Minimum 10s Mbps | Loss forward / reverse % |
|---|---:|---:|---:|---:|---:|
| sustained-https-300 | 300 | 1400 B | 349.956 | 348.342 | 0.013 / 0.000 |
| sustained-quic-reverse-120 | 120 | 1400 B | 349.422 | 346.687 | 0.000 / 0.166 |
| sustained-fault-duplex-90 | 90 | 1400 B | 336.103 | 228.586 | 3.979 / 3.963 |
| sustained-small-quic-30 | 30 | 512 B | 348.116 | 347.013 | 0.541 / 0.000 |
| final-steady-duplex-60 | 60 | 1400 B | 350.000 | 349.956 | 0.000 / 0.000 |
| final-https-reverse-60 | 60 | 1400 B | 349.731 | 348.932 | 0.000 / 0.078 |
| final-loss-quic-30 | 30 | 1400 B | 332.112 | 326.395 | 5.111 / 0.000 |

Fault throughput/loss includes the intentional all-path outage. The ordinary
10-second/whole-run gates do not apply to that case. The injected-loss case
applies 1% outer datagram loss, not 1% application loss. Final cases use queue
1024; cases prefixed `final-` in the table preceded that tuning and use 256.

With the final queue, the 90s outage test had a largest receiver gap of
3.493s during the intentional 3s all-path cut. Both carriers recovered,
the exit application source stayed unchanged, and the final five seconds
each delivered 346.9–349.9 Mbps aggregate. No duplicate/corrupt packets
were observed. Separate race-instrumented outage testing used 30 Mbps.

| Carrier, 8s measurement + 2s warmup | Before allocation MB | After allocation MB | Reduction |
|---|---:|---:|---:|
| https | 1661.14 | 519.88 | 68.7% |
| quic | 3634.30 | 2257.13 | 37.9% |

These are cumulative whole-harness allocations, not resident memory. The old
HTTPS run delivered about 189 Mbps while the optimized run delivers about 350;
both offered 350 Mbps. Profiles still attribute most QUIC allocations to
quic-go datagram copies. No zero-allocation or isolated-daemon memory claim is made.

[Machine-readable results and failed diagnostic runs](performance-results.json)
retain actual loss, latency, source digests and queue settings. Full local
logs and CPU/allocation profiles accompany the delivery package.
