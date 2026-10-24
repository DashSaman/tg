# Packet streams and TLS write batching: measured results

The confirmed next target is **1 GB/s = 8 Gbps aggregate**; the final 10 Gbps goal remains required. **Neither is achieved.**
The optional code is implemented and checked; capacity and stability verdicts below have separate scopes.

A clean same-binary off / on / on / off ABBA used kernel WireGuard MTU 60000, HTTPS isolated by blocking UDP, eight TCP streams and 12000 Mbps offered load. Two runs per flag on a shared VM are limited evidence:

| TLS batching | Mean receiver Mbps | Pooled TG CPU seconds/Gbit |
|---|---:|---:|
| Off | 1707.67 | 1.8928 |
| On | 2320.52 | 1.5180 |

TG process CPU per delivered Gbit fell **19.80%**; mean received throughput rose **35.89%**. Both enabled runs beat both disabled runs. This is a large-packet proxy/HTTPS comparison, not a general IP or kernel-CPU claim. CPU spans exclude the first 10 and last 5 seconds and use interpolated monitor counters; small launch-time offsets remain. Global VM CPU is counted once.

The current binary with both carriers enabled, MTU 60000 and 50 ms replica grace received **2855.85 Mbps**, minimum full 1 s **1273.26 Mbps** over 60 s. Its 8000 Mbps gate failed. A separate ordinary-IP flag pair is retained in JSON; it is not repeated causal evidence.

The fresh 30-minute ordinary-IP run offered 1000 Mbps with an 800 Mbps primary floor. Overall **FAIL**; the separately predeclared original 300 Mbps milestone is **PASS**. Steady receiver mean **1000.00 Mbps**, minimum full 1 s **457.71 Mbps**, minimum weighted 1 s **457.71 Mbps**. Repeated single-path cuts: mean **1000.01 Mbps**, minimum full 1 s **520.38 Mbps**. All-path outages are expected delivery gaps; individual 800/300 Mbps recovery checks and final carrier health are retained in JSON.

Loaded ping: 1500/1500 received, median=1.755 ms, p95=27.000 ms, maximum rounded reply RTT=193.000 ms, conservative timing coverage=True. The observed tail spike is retained even when median/p95 gates pass. RTT variation is not one-way or RTP/UDP jitter. TG memory measurements and post-cut growth:

| Role | Late RSS MiB | RSS growth MiB | PSS slope MiB/min | Memory gate |
|---|---:|---:|---:|---|
| edge | 24.459 | 0.045 | 0.0060 | PASS |
| exit | 23.129 | -0.297 | -0.0246 | PASS |

Auxiliary fault-phase pings covered all three cycles: single-path 170/170 received, p95=22.200 ms and maximum=45.100 ms; all-path 170/170 received, p95=28.200 ms and maximum=2677.000 ms. Percentiles and zero eventual loss do not remove the observed multi-second outage delay. Their ordinary low-latency targets and timing coverage are retained separately. These prospective diagnostics do not replace or change the primary 1500-ping steady gate.

The profile uses WG MTU 1500, max packet 2048, pre-encryption WG QoS 1200 Mbps, TG UDP output cap 1500 Mbps, replay 8192, default 10 ms backup grace, native stream windows, GRO off, both experimental options enabled, and GOMAXPROCS=4 per daemon on a 12-logical-CPU shared VM. This is paced local TCP, not unrestricted-load or real-Iran acceptance. Full strict verdicts, memory scopes, stable PID/socket checks, per-outage recovery and raw receipt hashes are in [JSON](stream-packets-results.json).

Host start/end timing for the steady phase spans 1820.963 s while receiver timing spans 1800.001 s, a 20.962 s difference. Launch/poll/end overhead and guest/host clock divergence are uncorrected. This does not prove the cause of the 29 sub-800 Mbps intervals. Long-run CPU/receiver alignment and per-cut timing are approximate diagnostics; the failed receiver speed verdict is preserved.

The 4.85 Gbps disposable prototype peak was **not reproduced**. A 16-record prototype repeat beat the 4-record repeat but neither reached 8 Gbps. Earlier packet-mode jumbo regression, window/grace tuning failures and the invalid one-carrier setup are preserved. Native WG with 60000-byte packets reached 12 Gbps on a jumbo private bridge; that is not a TG result. Increasing packet size does not enlarge ordinary client packets.

A later 256 KiB ciphertext read-ahead prototype received 2187.09 Mbps over HTTPS alone, 3181.32 Mbps with both carriers, and 1680.20 Mbps with ordinary IP. These were single runs, all failed 8 Gbps, and did not establish a repeatable improvement. It remains outside production source; its generated source and evidence are archived separately.

Correctness: all six Go packages under the race detector, go vet and 43 Python tests passed after measurement. Linux amd64/arm64 and Windows amd64 clean builds retain the runtime revision and hashes. New modes remain opt-in; see [configuration and bounds](stream-packets-design.md). No host routes, interfaces, qdiscs or sysctls were changed; lab changes were confined to owned disposable namespaces.

Remaining work includes repeatable 8 Gbps / 10 Gbps goodput, measured CPU on real Linux servers, ordinary-IP packet-rate bottlenecks and real path/filtering tests. A receive-batch path may reduce per-frame locking, but it is an unimplemented hypothesis requiring ownership/cancellation and performance tests.
