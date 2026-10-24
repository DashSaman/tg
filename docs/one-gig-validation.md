# Current 1 Gbps ordinary-IP and gaming validation

The current near-term user target is **about 1 Gbps aggregate**, continuous service,
low gaming latency/variation, and low CPU/RAM. The earlier 8/10 Gbps results remain
historical evidence. **Strict local acceptance for the present target: FAIL.**
The same runtime received 1.05 Gbps on average but did not sustain the declared
950 Mbps floor in every complete 1 s interval. It also missed the UDP gaming gates.
Real Iran-to-overseas acceptance remains untested.

## What was measured

Clean runtime `e9feb4fb4cb91f5b878f041dfe52edc362295102`, SHA256
`baf32eb0efe54a87c6682b1a6e5ac9c17e018f76d466c1f5ccc29d8c74383719`.
No Go/runtime behavior changed in this stage. The added UDP observer is byte-for-byte
the source used in the measurements; its three protocol/gate tests first failed
against stubs and then passed. All 49 shipping Python tests passed.

One isolated kernel WireGuard/TG pair, ordinary 1500-byte inner MTU, packet budget 2048,
replay 8192, unordered delivery, packet streams and TLS batching enabled, native
stream windows, GRO off. GOMAXPROCS=2 per daemon. CONNECT was preferred with
QUIC-stream also enabled. The 30 min diagnostic used replica_delay_ms=0, no TG
output cap and no WG QoS. Eight TCP streams offered 1050 Mbps aggregate, alongside
a 128-byte UDP echo flow targeting 50 Hz and 1700 additional monotonic ICMP probes.
The UDP sender avoids catch-up bursts; its actual send rate and missed slots are
reported, rather than claiming exact 50 Hz. All RTT subtraction uses Linux MONOTONIC.

The prospectively declared targets were 950 Mbps per full/weighted 1 s receiver window,
RTT median <20 ms, p95 <30 ms, adjacent RTT variation p95 <10 ms, zero probe loss, and no missed
UDP sender slots. These are local engineering gates, not a universal gaming standard.
UDP RTT/adjacent differences include scheduling and are not one-way or RTP jitter.
The bulk capacity result is TCP with a small concurrent UDP flow, not 1 Gbps bulk UDP proof.

## Complete results

| Phase | Mean receiver Mbps | Minimum full 1 s Mbps | UDP RTT p95 ms | Adjacent delta p95 ms | UDP loss |
|---|---:|---:|---:|---:|---:|
| steady | 1050.00 | 298.34 | 31.02 | 13.39 | 67/87311 |
| faults | 1046.80 | 335.59 | 44.02 | 15.86 | 9/8120 |
| all-path-faults | 1050.00 | 0.00 | 41.70 | 13.12 | 300/8249 |

The steady phase had **253 of 1800**
complete intervals below 950 Mbps; the weighted 1 s minimum was
300.66 Mbps. No sub-floor result was hidden by
averaging. Single-path cuts repeated three cycles, six outages total, with the
same TG processes and application sockets. Its final-five-interval speed recovery
gate failed even though both carriers were healthy at the end.

The mixed phase included three actual simultaneous protocol blackouts, with
guaranteed measured overlaps of 2.09 to 2.55 s. Zero goodput during these blackouts is
retained. After the first protocol reopened, five consecutive UDP replies sent
after reopening with RTT<30 ms were observed after
0.855, 1.010, 5.265 s. This is UDP latency recovery, not a
claim of sustained 950 Mbps recovery or uninterrupted service while all routes are blocked.
Both lab carriers share one local underlay; this does not prove disjoint WAN routes.

## Whole-stack resource accounting

The two private cgroups averaged **3.089 CPU cores**,
including TG, traffic generators, observers and charged kernel work. Global Linux
VM CPU, counted once, averaged **7.054 cores**;
this includes kernel WireGuard, softirq/unbound work and unrelated workloads.
It is not the physical Windows-host total or exact tunnel attribution. Separate
20 s no-tunnel background controls measured 2.818 and 1.008 busy cores, showing why
subtracting one short idle value would give a misleading precise tunnel cost.
CPU uses a common Linux MONOTONIC interior span; the stamp precedes iperf startup,
not hardware packet timestamps. CPU/runnable scheduling causes remain unresolved.

Whole-pair charged memory after warmup: early median **123.94 MiB**,
late **167.49 MiB**, peak **181.18 MiB**,
growth **43.54 MiB**, slope **1.795 MiB/min**.
The declared 1 MiB/min whole-lab trend gate failed; the 64 MiB growth gate passed.
This total includes iperf JSON accumulation and all observers. Same-PID PSS
snapshots show combined iperf rising from 25.87 to 51.52 MiB while TG PSS fell from
43.29 to 40.04 MiB between the two snapshots. This demonstrates a measurement-tool
contribution, not proof that it accounts for every byte of cgroup growth.
After the final trials/probes, the same private containers charged **76.53 MiB**.
TG late RSS summed **50.14 MiB**; both 30 min daemon growth/trend gates passed,
using monotonic samples. No leak-free guarantee beyond the measured interval is made.
Some global kernel allocations are not charged to these cgroups; VM memory is
reported separately in JSON and includes unrelated work.

## Confirmed loss location and remaining bottleneck

TG's internal dropped/app-error counters stayed zero, but this is insufficient
evidence of loss-free service. The edge application UDP socket 127.0.0.1:31820
overflowed before TG admission. Namespace receive-buffer errors rose by **67255**
over the whole test; stable socket-inode matching attributes the application-socket
portion in each phase. The actual receive buffer was 8 MiB, requested via 4 MiB SO_RCVBUF,
not silently capped at 212 KiB. WireGuard netdev errors and exit receive-buffer errors
were zero. The observed pre-admission loss is confirmed; the precise cause of read
lag is not. CPU pressure and runnable wait are diagnostics, not a hardware-only diagnosis.
Increasing queues can exchange loss for higher latency/RAM, so no host sysctl,
foreign-container or physical-interface change was made.

## Short controls and scope

| Control/phase | Mean Mbps | Minimum full 1 s Mbps | Edge app socket drops in interior span |
|---|---:|---:|---:|
| baseline/both-probe | 1050.03 | 1042.32 | 0 |
| procs1/both-probe | 794.03 | 429.88 | 866 |
| fault-preflight/steady | 1050.02 | 1037.18 | 515 |
| fault-preflight/faults | 1050.01 | 436.97 | 2236 |
| fault-preflight/all-path-faults | 1047.21 | 0.00 | 9987 |
| eager-copies/steady | 1050.00 | 1044.46 | 0 |
| eager-copies/faults | 1050.00 | 767.51 | 12067 |
| grace2/steady | 1049.95 | 558.41 | 2486 |
| grace2/faults | 1050.01 | 488.11 | 3759 |
| ordered/steady | 1050.02 | 595.91 | 977 |
| ordered/faults | 1044.13 | 696.29 | 3549 |
| qos1050/steady | 996.40 | 652.36 | 0 |
| qos1050/faults | 891.80 | 264.07 | 0 |

A final short control applied the existing HTB/FQ-CoDel profile at 1050 Mbps
before kernel WireGuard encryption, using the default 10 ms replica grace.
Actual qdisc configuration and packet activity were verified in both namespaces.
Steady UDP had 3847/3847 replies, RTT p95 8.30 ms and adjacent variation p95
5.06 ms, but four sender scheduling slots were missed. During the repeated
single-path cuts, UDP lost 3/8170 packets, RTT p95 was 29.27 ms and adjacent
variation p95 was 14.23 ms. The application-socket interior drop count was zero
in both phases, while the shaping qdisc reported 3285 drops over the control.
This shows a different congestion tradeoff, not a complete fix: its receiver
speed floor and gaming gates failed. Private-cgroup CPU averaged 2.812 cores
in steady traffic and 2.473 in the cut phase; global VM CPU was 5.451/7.156 cores.
The offered/received rates and background load differ, so no causal CPU-saving
percentage is claimed. This profile did not receive a new 30-minute acceptance run.
The raw control and its failed verdicts are retained in the JSON and bundle.

One GOMAXPROCS was rejected; default 10 ms grace, eager copies, 2 ms grace and ordered
output all retain their negative results. Eager copies had the best observed short
cut tails, but its long run did not reproduce a complete pass. This is not a causal
CPU percentage comparison. Delay 0 may send substantial duplicate traffic and is
not a default/WAN recommendation; physical ports need room for encryption and copies.
The runtime defaults remain unchanged.

See [machine results and receipt hashes](one-gig-results.json),
[Persian report](one-gig-fa.md), and the shipped bounded
[UDP observer](../tools/wgsoak/game_udp_probe.py). Full raw JSON, per-packet rows,
actual monotonic iptables operation bounds, frozen drivers/observers, cleanup and
source/binary fingerprints are included in the separate delivery bundle.
All owned private containers/networks/credential volumes were removed. No real
Iran route, DPI resistance, public port capacity or 8/10 Gbps stability is claimed.
