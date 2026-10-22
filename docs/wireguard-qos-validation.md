# Complete local WireGuard queue-control acceptance

**Local speed, memory, loaded latency and recovery acceptance passed under
350Mbps aggregate TCP offered rate.** This does not certify the failed unpaced saturation condition. Tested runtime
`bba3467e17a1ff29e37ae1214de8073b886fe614`, clean VCS build; Linux amd64 SHA256
`8c616a6b44e44d2c54b192b853fd38529ee87b57ce028a105ce3456c81fbe130`. [Machine-readable result](wireguard-qos-results.json).
This supersedes the short queue-control probe; the [failed previous full control](wireguard-c9-control.md)
is retained with its actual negative result.

| Phase | Duration | Average Mbps | Minimum full 1s Mbps | Minimum weighted 1s Mbps | Full 1s below 300 | Verdict |
|---|---:|---:|---:|---:|---:|---|
| Steady | 1800.00s | 350.00 | 324.33 | 324.34 | 0 | PASS |
| Repeated single-path cuts | 180.01s | 350.01 | 340.39 | 340.39 | 0 | PASS |
| Mixed including all-path outages | 180.00s | 347.67 | 0.00 | 0.00 | 11 | Speed FAIL; recovery PASS |

The configured deferred-copy preference was **https**.
The load generator used **350Mbps aggregate TCP offered rate**, not a different receiver speed floor.
The unpaced stress controls are retained with their actual failures; a paced
acceptance does not establish the same minimum under unrestricted saturation.
Linux UDP segmentation was enabled with bounded storage and an automatic
unsent-suffix fallback for unsupported kernels; actual counters are preserved
in the raw daemon snapshots. This does not alter the carrier wire protocol.

Every complete approximately one-second receiver interval and weighted one-second
window is checked against 300 Mbps for steady traffic and single-path failures.
No ten-second smoothing replaces this criterion. The mixed phase intentionally
blocks every carrier for three seconds in each cycle; its zero-throughput windows
remain recorded as speed failures. All three cycles recovered above 300 Mbps.
An outage of every available path cannot provide uninterrupted delivery.

The runtime also includes the reproduced [application duplex-lock fix](application-duplex-lock.md):
writer backpressure no longer holds the peer-update mutex needed by the opposite
input direction. A real-UDP regression timed out after one second on the old
synchronization and completes immediately with separate writer and peer locks.
Writer deadlines, reusable buffers and in-flight peer snapshots stay protected.
The earlier [full 64-frame control](wireguard-batch64-control.md) remains failed.

The same two TG processes and exit UDP socket survived all phases. Each 180s
single-path phase has three cycles, dropping UDP at seconds 10–20 and TCP at
35–45 of each cycle; the mixed phase additionally drops both at 40–43.
Independent server-side iperf JSON matches the client-embedded receiver evidence.

1500 ICMP probes during steady bulk traffic: all received, median 1.270ms,
mean 2.319ms, p95 6.930ms, maximum 36.500ms.
The prospective target was zero ICMP loss, median below 20ms, p95 below 30ms.
These are loaded round-trip measurements, not an Internet ping guarantee or a
claim that congestion control never drops data. FQ-CoDel deliberately drops/marks
congested traffic. Occasional RTT spikes remain visible in raw evidence.

| TG process | Late median RSS MiB | RSS growth MiB | RSS slope MiB/min | Late median PSS MiB |
|---|---:|---:|---:|---:|
| edge | 21.484 | 0.391 | 0.0146 | 17.531 |
| exit | 23.621 | 0.078 | -0.0051 | 19.668 |

Memory compares minutes 5–10 with the final five minutes, samples every five
seconds, and checks both RSS/PSS growth and slope. This measures TG processes;
kernel memory, monitor and iperf JSON history are separate. No leak-free claim
beyond the observed duration. Fresh Go race tests, vet, Python acceptance/QoS/
fault-schedule tests and source/binary identity checks passed after measurement.
All disposable lab containers, network and private credential volume were removed.

## Profile and reproduction

The host was a shared 12-logical-CPU WSL2 Linux VM; GOMAXPROCS=4 per TG process,
not a certified four-vCPU machine. Eight TCP streams, 128KiB writes,
kernel WireGuard MTU 1280, authenticated QUIC-stream plus HTTPS/2, bounded shared
sender log, 10ms deferred backup copies, independent completed UDP output and
500 Mbps TG ciphertext output pacing. The optional explicit WireGuard interface
profile is HTB rate/ceil 400 Mbps with FQ-CoDel target 5ms, interval 100ms, limit 1024
and 4MiB queue memory. Both peers' actual rate, qdisc settings and packet activity
were verified before/after; this is not merely an intended configuration.

```sh
python tools/wgsoak/run.py --binary /absolute/path/tg-linux-amd64 --out /absolute/path/new-results --seconds 1800 --fault-seconds 180 --skip-https-probe --gomaxprocs 4 --redundant --window 1 --fault-plan single-path --extra-mixed-faults --queue-frames 1024 --udp-mode quic-stream --mbps 350 --app-pacing-mbps 500 --wg-qos-mbps 400 --preferred-carrier https
```

See [safe explicit queue-control setup](wg-qos-profile.md). The local delivery
bundle contains exact binaries, tested and delivery source archives, raw receiver
JSON, monitor data, fault receipts, ping, independent analysis, correctness logs,
CI identity, retained negative controls and SHA256 manifests. The combined rate
and queue profile is evaluated as a whole; these measurements do not isolate the
effect of FQ-CoDel alone. The profile trades some peak throughput for latency and
the sustained floor. Iran WAN, DPI resistance, reverse/symmetric capacity, 10Gbps
and 24h operation still require separate tests.
