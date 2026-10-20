# Full-stack local WireGuard validation — 2026-10-20

**The 300 Mbps full-stack acceptance gate failed.** The earlier short opaque-UDP
tests do not establish this TCP-over-WireGuard result. No production data-path
change was made during this validation.

## Method

The unmodified Linux amd64 TG executable from runtime commit
`ea08a315fde8d501161403db93558b3a432e2c27` ran in two separate Docker network
namespaces on the same Linux VM (12 logical CPUs, about 20 GiB RAM). Each namespace
had a real kernel WireGuard interface, MTU 1280. The data path was eight TCP streams
between inner IPv4 addresses, through WireGuard -> TG -> WireGuard. iperf3 3.12
requested 43.75 Mbps per stream, 350 Mbps aggregate. TG queue size was 1024.

Receiver intervals and byte totals came from the independent iperf server JSON;
the embedded receiver output was checked against it. The steady measurement lasted
1800.003709 seconds and delivered 44.946 GB. Acceptance required average and every
complete rolling 10-second receiver window >=300 Mbps. The rate gate was kept.

The same TG processes and WireGuard interfaces then carried another 180 seconds
of TCP load. Three cycles blackholed the outer UDP carrier, then TCP carrier, then
both. Nominal cuts were 6s, 6s and 3s, respectively; Docker command latency means
the delivered event timestamps are authoritative. Controls applied only inside the
edge container's network namespace. No host routes, firewall rules, public listeners
or global sysctls were changed.

## Results

| Measurement | Receiver average Mbps | Minimum rolling 10s Mbps | Outcome |
|---|---:|---:|---|
| WireGuard directly, 30s control | 350.08 | 349.80 | Short baseline passed |
| WireGuard + TG, 1800s steady | 199.76 | 74.19 | Target failed |
| Same processes, 180s loaded outage cycles | 162.87 | 19.66 | 300 Mbps recovery target failed |
| Both carriers, unpaced TCP, 30s control | 206.96 | 190.74 | Target failed |
| QUIC alone, paced TCP, 30s control | 207.56 | 173.34 | Target failed |

Both carriers were healthy after all cuts. TCP connections survived and the exit
application socket and TG process identities stayed unchanged. Functional path
recovery is distinct from recovery to the requested bitrate: the latter failed.
The longest contiguous run of zero-byte receiver intervals was about 6.00 seconds;
this uses approximately one-second iperf reporting, not exact packet-gap timing.
Steady TCP sender retransmissions numbered 529,894. There were 24,855 recorded
edge carrier changes during the steady run. These are diagnostic signals, not proof
of one root cause. Larger queues, smaller iperf write blocks, removing the pacing
limit, and disabling the second carrier did not individually achieve the target.

## Resident memory

An external monitor sampled each TG daemon's Linux `/proc/PID/smaps_rollup`
RSS/PSS about every five seconds. It retained samples outside TG; TG did not retain
the growing report. There were 356 usable samples per daemon spanning the run,
with process start-time identity and sample-gap checks. No forced GC was used.

| TG daemon | RSS median, minutes 5–10 MiB | RSS median, last 5 minutes MiB | RSS growth MiB | Sampled peak RSS MiB |
|---|---:|---:|---:|---:|
| edge | 16.098 | 16.203 | 0.105 | 17.004 |
| exit | 18.012 | 18.041 | 0.029 | 22.211 |

PSS median growth was 0.096 MiB / 0.015 MiB. PSS slopes after minute five were
0.0047 / 0.0100 MiB per minute. Final analysis gates both RSS and PSS slope at
0.5 MiB/min and early-to-late median growth at 16 MiB; both daemons passed these
engineering gates. This is bounded observation at the actual measured traffic rate,
not a proof of no leaks at all loads or beyond 30 minutes.

Whole-container memory grew because it includes iperf's retained JSON interval
records, the Python monitor, socket buffers and charged kernel memory. Independent
process snapshots separated this from TG: iperf edge RSS rose from about 18.2 to
33.8 MiB and iperf exit RSS from 14.0 to 24.7 MiB between the two snapshots.
Charged kernel median changed by about 0.05 / 0.04 MiB; edge charged socket memory
rose about 3 MiB. These counters do not account for every global kernel allocation.

The final memory analysis also checks sustained RSS growth, and the final recovery
gate requires full-duration coverage and five actual final receiver intervals.
Regression tests reject RSS-only growth, truncated receipts and a slow last full
interval. Original measurement-time gate results remain in the delivery alongside
the stricter postprocessing and raw samples; no measurement was replaced.

## Reproduce

On a Linux Docker host, build the normal TG executable and the disposable lab image:

```sh
go build -trimpath -o bin/tg ./cmd/tg
docker build -t tg-wgsoak:local tools/wgsoak
python3 tools/wgsoak/run.py --binary bin/tg --out receipts/wg-30min \
  --seconds 1800 --fault-seconds 180 --skip-https-probe
python3 -m unittest discover -s tools/wgsoak -p 'test_*.py' -v
```

The output directory must not already exist. Containers receive only NET_ADMIN;
the lab uses an internal Docker network, no published ports and temporary generated
keys in an owned volume. Cleanup removes the lab containers, network and private
volume; inspect `cleanup.json`. The image remains for reproduction. Container-only
package installation uses HTTPS Debian repositories. Kernel WireGuard support is
required; interface creation fails explicitly when unavailable.

Use `--probe-only direct --seconds 30` for the direct WireGuard control,
`--probe-only both --mbps 0 --seconds 30` for unpaced TCP, and
`--probe-only quic --carrier quic --seconds 30` to isolate QUIC. Short probes are
explicitly separate from 30-minute acceptance. Probe speed failures retain receipts
and return nonzero. CI runs the fast analyzer tests, not the privileged long lab.

[Numerical results](wireguard-results.json) are committed. The delivery includes
independent client/server JSON, events, complete memory time series, the actual
running harness snapshot, hardened analysis source, a Persian report and a plotted
throughput/memory timeline. Temporary private keys are excluded. Configuration
follows the [WireGuard quick start](https://www.wireguard.com/quickstart/).

Real Iran/overseas path quality, loaded application jitter, 24-hour endurance and
10 Gbps scaling remain unverified. The next performance diagnosis must distinguish
datagram loss, inner TCP retransmission/reordering, carrier pressure switching and
cross-process network I/O costs before another full-stack acceptance attempt.
