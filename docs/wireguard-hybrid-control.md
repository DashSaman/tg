# Full GSO/HTTPS hybrid control: speed and zero-loss acceptance FAILED

Runtimebef9e497e62b95638c3a55e2345a136496e6d6df, clean uninstrumented Linux
amd64 build. The full1800+180+180s experiment used eight paced TCP streams,
350Mbps offered aggregate,128KiB writes, WG MTU1280, Go4 per TG on the shared
12-logical-CPU WSL2 VM; HTTPS cost0,QUIC-stream cost20,10ms deferred backups,
shared sender log, independent UDP delivery, TG output cap500Mbps and actual
WG HTB400/FQ-CoDel5/100ms/1024packets/4MiB at both peers.
[Compact machine-readable evidence](wireguard-hybrid-control-results.json).

| Phase | Average Mbps | Minimum full1s Mbps | Bad full seconds |
|---|---:|---:|---:|
| Steady1800s |350.001|206.032|9|
| Repeated single-path180s |350.001|328.938|0|
| Mixed including all-path outages180s |347.027|0|13|

Single-path complete and weighted1s speed checks passed. The mixed phase kept
expected zero windows and all three cycles recovered. Steady speed FAILED.
1500 loaded ICMP probes:1498 replies,0.133333% loss,median1.50ms,p959.51ms,
maximum251ms; population RTT deviation9.029ms. Missing sequences996 and1084.
These are1Hz round-trip observations, not one-way UDP stream jitter. The
prospective zero-loss condition FAILED even though median/p95 passed.

TG memory passed: late RSS medians21.930/23.141MiB, early-to-late growth
0.209/0.055MiB, RSS slopes0.0157/0.0126MiB/min at edge/exit. PID and application
socket identity stayed stable. Native GSO accepted over71million exit packets
without a fallback. TG process memory excludes kernel/monitor/iperf memory.
Fresh Go race tests/vet,31Python tests and source/binary identity checks passed.
All private lab containers, network and credential volume were removed.

## Matched native WireGuard bypass control

A subsequent600s control kept WG, the host, QoS400, eight offered350Mbps TCP
streams,128KiB writes and MTU1280, but routed ciphertext directly between kernel
WG endpoints. TG input remained564/476 bytes while WG transferred about28GB;
this verifies the bypass. Independent receiver JSON matched the embedded copy.
Average350.001Mbps, minimum full/weighted1s345.138Mbps, no bad seconds. All500
concurrent pings returned: median0.776ms,p956.97ms,max11.8ms. Cleanup was empty.
This is a ten-minute control, not a matched-duration30-minute proof or evidence
that every earlier TG loss has one cause. It did not reproduce the TG floor dip.

## Next bounded optimization

The old16-frame carrier cap allows at most eight1312-byte WG UDP packets per
write (fewer when control frames share the vector). Measured full-run exit GSO
averaged about7.1packets per accepted GSO message despite the32-message UDP
vector. The next change raises only the internal frame vector to64, enough for
32 such WG packets, without waiting to fill a batch. Framing memory remains
bounded. QUIC DATAGRAM's original16-frame wire-bundle limit remains separate;
large internal vectors split into compatible datagrams. Unit/race success is
not throughput acceptance; a new measurement is required.

Zero socket/interface counters do not exhaustively exclude internal WG drops.
The upstream[v6.18.33 receive path](https://raw.githubusercontent.com/gregkh/linux/v6.18.33/drivers/net/wireguard/receive.c)
and[queue helpers](https://raw.githubusercontent.com/gregkh/linux/v6.18.33/drivers/net/wireguard/queueing.h)
contain queue/replay rejection paths which free packets without those interface
counters. This is a diagnostic limitation, not confirmation that these two ICMP
losses occurred there, nor verification of every WSL-specific kernel patch.
No Iran WAN, DPI,10Gbps,24h or unpaced-saturation stability certification.
