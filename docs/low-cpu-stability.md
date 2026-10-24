# CPU-optimized runtime: fresh full WireGuard test

**30-minute speed/memory/recovery: PASS. Original combined long-run
latency-coverage verdict: FAIL. Separate 120-ping latency component: PASS;
its combined receiver-speed control: FAIL.
Required 10 Gbps remains unachieved.**
This result uses runtime `b772c067e611683f491ec86688b448771f01618a`, clean VCS build, Linux amd64
SHA256 `a68ec33597e60f471f2d30abd19ce16f4268e46a6bf128538d89ff95071731b9`. [Machine-readable result](low-cpu-stability-results.json).
The [earlier accepted runtime/profile](wireguard-qos-validation.md) is retained
separately. [CPU and failed capacity comparisons](low-cpu-capacity.md) establish
the 18.10% measured TG CPU-cost improvement at 800 Mbps; the different profile
below is a fresh stability test, not a causal CPU comparison against that run.

| Phase | Duration s | Receiver mean Mbps | Minimum full1s Mbps | Minimum weighted1s Mbps | Full1s below300 | Speed | Recovery |
|---|---:|---:|---:|---:|---:|---|---|
|Steady|1800.00|350.00|327.23|327.23|0|PASS|N/A|
|Repeated single-path cuts|180.00|350.01|327.84|327.84|0|PASS|PASS|
|Mixed including three all-path cuts|180.00|347.30|0.00|0.00|12|FAIL|PASS|
|Separate covered-ping speed control|180.00|350.01|191.63|11.47|3|FAIL|N/A|

The unchanged 300 Mbps gate checks every full approximately 1s receiver interval
and every weighted1s window. All raw intervals, including zero throughput while
both carriers are blocked, remain recorded. The mixed phase is evaluated for
recovery; it cannot meet uninterrupted speed during an all-path outage. Each
180s fault phase repeats three 60s cycles. Independent server-side iperf results
match the client-embedded receiver evidence; sender nominal rate is not goodput.

## Actual load and resource scope

Eight TCP streams, 128 KiB writes, 350 Mbps aggregate offered load; WG MTU 1500 on
both actual interfaces, max TG packet 2048, shared acknowledged replay 8192,
independent UDP output, 10ms backup grace, HTTPS cost 0/QUIC-stream cost 20,
GRO off/native stream windows. Pre-encryption WG HTB 400 Mbps/FQ-CoDel 5ms target,
100ms interval,1024 packets/4MiB queue budget; TG cipher output pacing 500 Mbps.
Both peers' actual configurations, qdisc settings/activity and source fingerprints
are checked. The load condition and queue profile are part of this result.
Failed unrestricted-overload capacity controls remain failed; this does not
certify the same floor at unlimited offered load. 4 Go processors per TG process
on a shared 12-logical-CPU VM is not a certified four-vCPU server result.

| TG process | Late median RSS MiB | RSS growth MiB | RSS slope MiB/min | Late median PSS MiB | PSS growth MiB |
|---|---:|---:|---:|---:|---:|
|edge|23.336|0.211|0.0067|19.361|0.199|
|exit|24.066|0.070|-0.0029|20.092|0.061|

Memory compares minutes 5–10 against the final five minutes, with 5s samples,
RSS/PSS growth and post-warmup slopes. Process/socket continuity and additional
memory behavior through cuts are checked separately. These are sampled TG
process measurements, not kernel/NIC/iperf memory or an indefinite leak guarantee.

Steady CPU after the first five minutes: edge **0.829** cores, exit **0.937**; combined **5.044 TG CPU seconds/Gbit**. The shared Linux VM averaged **3.488** busy cores, counted once and including kernel, iperf and unrelated work. Monitor-to-receiver timing retains a small uncorrected launch offset. This is not physical Windows-host CPU.

Original ICMP observations: **1500/1500** replies, median **1.160ms**, p95 **6.980ms**, rounded sample maximum **31.000ms**. RTT standard deviation **2.408ms** is a 1 Hz round-trip variation proxy, not one-way or RTP/UDP-user jitter. **Conservative coverage FAILED:** probe metadata starts 0.465s after announced load, earlier than the required +1s guard. Numeric targets pass but the original combined acceptance remains false; no first sample was silently removed.

A separately declared 180s control with the **same binary, harness and 350 Mbps profile** starts 120 one-Hz pings 5s after announced load. Covered latency component **PASS**: **120/120** replies, median **1.185ms**, p95 **6.130ms**, maximum **8.080ms**; the same zero-loss/median<20ms/p95<30ms targets and+1s/end-1s guard apply. **The accompanying receiver-speed control FAILED**, with three full intervals below 300 Mbps and six irregular nonterminal intervals; the failed rates are in the table. Independent receiver /proc IO also shows delivery dips, so they are not discarded as an iperf reporting artifact. Coincident sampling gaps in multiple processes suggest scheduling/clock/host behavior worth tracing, but the cause is unresolved. The carriers stayed healthy and TG application error/drop counters remained zero. This separate latency component does not replace either the failed 1500-ping coverage condition or this new speed failure. Repeated stable-floor behavior therefore remains unproven despite the successful long run.

Fresh Go all-package race tests, vet, Python acceptance/QoS/memory/fault tests,
binary/source identity and private-lab cleanup checks accompany the raw result.
Linux amd64 was tested under load; Linux arm64/Windows binaries were cross-built.
TG does not alter host routes or host qdiscs. Each lab owns only its private
containers/network/credential volume. There are no generated keys in delivery.

## Reproduce

Use the exact tested binary and a Linux Docker host with kernel WG and HTB/FQ-CoDel;
build `tg-wgsoak:local` from tools/wgsoak/Dockerfile first. Avoid competing builds
or benchmarks during measurement. The 1500 loaded pings run concurrently as a
separate probe; their prospective plan and raw log are in the delivery bundle.

```sh
python tools/wgsoak/run.py --binary /absolute/path/tg-linux-amd64 --out /absolute/path/new-results --seconds 1800 --fault-seconds 180 --skip-https-probe --gomaxprocs 4 --redundant --window 1 --fault-plan single-path --extra-mixed-faults --udp-mode quic-stream --mbps 350 --target-mbps 300 --app-pacing-mbps 500 --wg-qos-mbps 400 --preferred-carrier https --mtu 1500 --max-packet-bytes 2048 --replica-delay-ms 10 --retained-packets 8192 --stream-window-bytes 0
```

Ordinary packet sizes here do not exercise public-IP forwarding/NAT or certify
UDP user-load capacity. Proxy jumbo profiles are separate. Iran WAN/filtering,
high RTT, reverse/symmetric capacity, 10 Gbps and 24h operation remain unvalidated.
An all-path/IP block always interrupts delivery. [Independent workers](independent-workers.md)
provide separate session provisioning; more workers did not improve this VM's
measured capacity and do not themselves prove 10 Gbps support.
