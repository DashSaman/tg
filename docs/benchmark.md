# Receiver acceptance protocol

Latest follow-up (2026-10-22): [repaired full-stack WireGuard](wireguard-fixed-validation.md)
passed the local 30-minute receiver and TG memory gates: 350.00 Mbps
average / 303.59 Mbps minimum ten-second window, plus repeated
loaded outage recovery. Earlier results below retain their historical scope.

The completed local kernel-WireGuard test is recorded in
[wireguard-validation.md](wireguard-validation.md). Its 30-minute receiver-speed
gate failed. The procedure below remains the future real-host acceptance protocol.

This is a future real-host procedure, not a measured TG result. Use owner-provided
Iran and overseas hosts; record CPU/NIC/link limits, kernel, TG commit, MTU, UTC time,
ISP and both directions. Measure the direct baseline first, then inner WireGuard
over each TG carrier separately and with both enabled. One IP is one shared block
domain. Do not classify every outage as filtering.

## Throughput and completeness

Run iperf3 at the **inner application endpoints** (example inner exit IP 10.77.0.2).
Do not point TCP iperf directly at TG's UDP socket. WireGuard/routing must already
work. Bind iperf only to the test interface and restrict access. Start a new one-shot
server for each run:

```sh
# Exit application host:
iperf3 -s -1 -J > server-forward.json
# Iran application host:
iperf3 -c 10.77.0.2 -P 8 -t 1800 -i 1 -J --get-server-output > client-forward.json
python3 tools/acceptance.py client-forward.json
```

Repeat with `-R` and a new server; preserve raw server JSON independently. iperf's
intervals belong to the endpoint generating them; forward client intervals are
sender measurements. The analyzer chooses explicit receiver intervals from the
root or embedded `server_output_json`, rejects absent/partial/malformed intervals
and iperf errors, and ignores sender-advertised bitrate. See the
[official iperf manual](https://github.com/esnet/iperf/blob/master/docs/invoking.rst).

Default gate: at least 1800 measured seconds, aggregate receiver average >=300 Mbps
and every complete rolling 10s window >=300 Mbps. Windows start at interval boundaries
and weight boundary bytes by overlap; use approximately 1s reporting. Do not pass a
short run by lowering duration and then describe it as 30-minute acceptance.
Exit code 0=pass, 1=threshold failure, 2=invalid/unsupported receipt. This gate measures
aggregate traffic, not a required single-flow rate. Ordinary final JSON is supported;
JSON streaming and bidirectional mode are not. Analyze directions separately.

For UDP loss/jitter, repeat with `-u -P 4 -b 100M -l 1200`; offered load is per stream.
Start lower and increase gradually to avoid making overload look like path failure.
Default provisional limits: maximum receiver interval loss 1%, jitter 20ms, alongside
the goodput gate. Report per-stream details too: an aggregate jitter statistic can
hide a bad stream. TCP JSON alone cannot certify application packet jitter; a null
jitter result is not zero. Thresholds are engineering starting points, not universal
quality guarantees. The analyzer's automated tests use fixtures; real iperf version
compatibility must be checked against receipts from the supplied test hosts.

## Stability

Record application latency distribution under idle and bulk load, receiver loss,
TG frame/queue/drop counters, CPU per core and memory. Capture a 24-hour soak and
repeat at multiple hours/ISPs before declaring Iran stability. Use application
probes through the real exit; carrier `/healthz` is insufficient.

Controlled test matrix on disposable test hosts: UDP-only blackhole, TCP reset,
both unavailable, 1/3/5% loss, 50/150/300ms additional delay, jitter, reordering,
bandwidth caps, edge restart and exit restart. Record detection time, receiver
interruption and recovery separately. TG currently preserves exit socket only while
the exit process stays alive; it does not promise lossless failover. All-path outage
is an availability failure, not something to hide in the throughput average.

Keep controls isolated and restore them afterward. No impairment command is run by
TG itself. Future 10Gbps work requires profiling socket I/O, packet batching,
QUIC DATAGRAM limitations, allocations and multi-session sharding before scaling
claims. No 10Gbps architecture guarantee follows from this prototype.
