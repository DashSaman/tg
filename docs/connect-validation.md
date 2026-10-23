# CONNECT measurements and remaining capacity work

Runtime `e9feb4fb4cb91f5b878f041dfe52edc362295102`. 8 Gbps stable acceptance: **FAIL**. 10 Gbps: **not accepted**.

All measurements used actual kernel WireGuard and TG in private containers on one shared 12-vCPU Linux Docker VM. The Windows host reports 8 physical / 16 logical CPUs. No WAN or Iran filtering was tested. Proxy 60000-byte virtual WG MTU and ordinary 1500-byte routed-IP profiles must not be combined.

## Clean single-pair controls

The two CONNECT proxy samples received 7594.45 / 7833.21 Mbps and used 0.42329 / 0.39418 TG CPU seconds per delivered Gbit. HTTPS/2 controls received 2330.96 / 2367.64 Mbps and used 1.48770 / 1.46115 CPU seconds per Gbit. The second H2 control resumed in a separate session after an interrupted run with empty results. These observations support the protocol choice; they do not establish an uninterrupted ABBA or 30-minute acceptance at 8 Gbps.

For ordinary IP, HTTPS/2 received 1732.05 / 1576.06 Mbps, compared with 2079.21 / 1701.97 Mbps for CONNECT. TG CPU seconds per Gbit were 2.40642 / 2.51311 and 1.37247 / 1.62904 respectively. Throughput ranges overlap; both CONNECT CPU observations are lower. Every 8 Gbps gate failed.

## Independent flow groups and short controls

Each group has distinct WireGuard and TG sessions; users or flows must be assigned before encryption. There are 16 TCP streams in total, eight per group. Encrypted WG packets are not striped across peers. Summed means do not establish exactly aligned one-second windows at 8 Gbps.

| Control | Sum mean Mbps | TG CPU s/Gbit | Both 4 Gbps floors |
|---|---:|---:|---|
| tg-connect-two-pairs | 8761.61 | 0.50341 | FAIL |
| tg-connect-groups-procs3 | 8269.87 | 0.54092 | FAIL |
| tg-connect-groups-paced4300 | 8576.79 | 0.51007 | FAIL |
| tg-connect-groups-qos4500 | 7882.84 | 0.54494 | FAIL |
| tg-connect-groups-procs1 | 8180.01 | 0.37569 | FAIL |

The first 90-second run reached **8761.61 Mbps = 1.095 GB/s**, using 4.41 combined TG process cores. P95 loaded RTT was 31.5 / 34.2 ms; both latency gates failed. The lowest complete one-second rates were 3069.56 / 3217.15 Mbps per group. Global Linux VM CPU usage averaged 8.52 cores, counted once and including kernel and unrelated work. It does not measure TG alone or the physical Windows host.

## Full 30-minute run and faults

The test offered 4300 Mbps per group and required 4000 Mbps per group in every complete and rolling one-second window. The sum of receiver means was **7735.35 Mbps**. Both floor gates: **FAIL**. Steady latency and variation: **FAIL**. All steady TG memory gates: **PASS**.
Combined TG CPU usage averaged 4.141 cores, or 0.53545 CPU seconds per delivered Gbit. CPU timing alignment is approximate.

The same four TG processes continued through 180 seconds of single-path faults and 180 seconds of mixed faults per group. Three single-path cycles contain six individual cuts; three mixed cycles include three complete outages. The primary 4 Gbps gate and the original 300 Mbps milestone are reported separately. Recovery requires five consecutive complete intervals within 15 seconds. Deliberate total-outage gaps remain in the results.

Both groups missed the 4 Gbps recovery deadline after the second complete outage. All three restorations per group passed the separate 300 Mbps recovery check. These are approximate host-to-receiver time mappings, not precise outage-latency measurements.

| Group | Steady mean Mbps | Min complete 1 s Mbps | Legacy P95 ms* | Legacy max ms* | Legacy adjacent delta P95 ms* |
|---|---:|---:|---:|---:|---:|
| 1 | 3871.47 | 782.01 | 46.600 | 128.000 | 27.900 |
| 2 | 3863.89 | 622.71 | 46.200 | 122.000 | 28.300 |

The 1500-probe iputils logs contain backward wall-clock warnings. **Their RTT timing is INVALID for acceptance**, even where numerical percentiles look acceptable. All original numbers and warnings are retained. The installed iperf3 3.12 `iperf_time_now` disassembly explicitly calls `clock_gettime(CLOCK_MONOTONIC)` (Linux clock ID 1), so this wall-clock failure does not directly invalidate the receiver bitrate timer or establish the cause of rate dips. Raw disassembly is archived.

The declared targets were median RTT below 20 ms, P95 below 30 ms, zero loss, and adjacent RTT-difference P95 below 10 ms. These measure round-trip variation, not one-way or UDP/RTP jitter. Another 170 pings observe each fault phase; coverage and clock validity are checked separately. Eventual zero loss can coexist with multi-second outage delays. Full maxima and fault-phase observations are in JSON.

| Group/role | Late RSS MiB | RSS growth MiB | PSS slope MiB/min | Steady memory |
|---|---:|---:|---:|---|
| 1/edge | 66.215 | +0.668 | -0.0333 | PASS |
| 1/exit | 40.094 | -0.746 | -0.0047 | PASS |
| 2/edge | 62.803 | +4.279 | +0.1951 | PASS |
| 2/exit | 40.387 | +0.023 | -0.0052 | PASS |

Memory gates allow at most 16 MiB median growth and 0.5 MiB/min RSS/PSS slope after 5 min warmup. Post-fault growth and container charged memory are reported separately in JSON; these observations do not prove absence of leaks beyond the run. Host pre-launch/end times, actual receiver durations and approximate per-cut alignment are retained. First server starts are gated; client launch times are recorded without a barrier inside the measured pre-launch span.

## Independent monotonic latency control

After the 30-minute run, a separate 360-second control used the same clean binary and proxy profile, offering 4300 Mbps per group. Each group collected 300 ICMP probes using `time.monotonic_ns` for all RTT subtraction. Cookie, identifier, sequence, checksum and load timing were validated. The sum of receiver means was **6652.89 Mbps**. This short control does not replace the missing valid 30-minute RTT observation or the failed 8 Gbps floor.

| Group | Received/requested | Median ms | P95 ms | Max ms | Adjacent delta P95 ms | Load guard |
|---|---:|---:|---:|---:|---:|---|
| 1 | 300/300 | 29.969 | 58.092 | 84.242 | 36.404 | PASS |
| 2 | 300/300 | 27.446 | 53.057 | 87.922 | 33.071 | PASS |

## Rejected small-packet experiments

Three default-off, bounded prototypes tested early backup copies, separate receive/output priority, and fuller small-only UDP batching. Unit ownership, queue bounds, cancellation/fairness, race tests and 46 Python checks passed for the final candidate, but the required combined speed/latency/CPU result did not. Every source patch, exact source snapshot, binary hash, and raw ABBA is archived. **None of those experimental flags or queues is shipped.** Reverting only verified agent-owned edits restored the exact CONNECT runtime; fresh 6 Go race packages, vet and 44 Python tests passed.

## Evidence and use

[Machine-readable results](connect-results.json), [CONNECT design and configuration](https-connect-design.md), [Persian report](connect-fa.md). Receipt directories in JSON refer to the separate delivery archive; no private key or token volume is included. Three clean cross-builds and the source revision are fingerprinted. Iran-path filtering resilience and 10 Gbps capacity remain unverified. Capacity, latency, recovery and memory need to be measured together on the actual two servers.

The checked-in `tools/wgsoak/icmp_monotonic.py` is byte-for-byte the observer used in the 360 s control. It is for the isolated lab (wg-tg, 10.77.0.1 to 10.77.0.2) and writes to /out. With an active owned lab edge container, run `docker exec <edge-container> python3 /tools/icmp_monotonic.py --count 300 --stem ping-monotonic`. Delivery Python tests: 46 passed; Go runtime and the existing lab harness stayed identical to the measured e9 revision.

Post-cut TG RSS increases ranged from 2.25 to 13.95 MiB across the four processes. Container charged-memory peaks were 82.79–143.86 MiB each, including processes and charged socket/kernel memory. These are separate from daemon RSS. Each application socket inode stayed constant and 19 WireGuard handshake timestamps were observed per role.
