# Repaired full-stack WireGuard validation — 2026-10-22

The completed local 30-minute test **passed**: receiver average
**350.00 Mbps**, minimum rolling ten-second window
**303.59 Mbps**, with bounded TG process memory.
The same two processes then completed three loaded outage cycles and recovered.
This replaces the failed local performance conclusion, not the retained baseline
data. [Machine-readable results](wireguard-fixed-results.json).

## Frozen runtime and method

- Runtime commit: `eef5f85f4aa7ee571fec69092be0659776f36f9e`; clean VCS build with Go 1.26.10.
- Tested Linux amd64 binary SHA-256: `5a0e613ea3427a6058bc9cfa9dc05b919de046ac561d1ca7ae8f36d63e43657b`.
- Actual kernel WireGuard, MTU 1280, eight inner TCP connections, offered aggregate
  350 Mbps, 1800.000165 receiver seconds, 1024 TG frame queue.
- Default reliable `quic-stream` (`tg-stream-v1`) and HTTPS/2 backup, mutual TLS on
  QUIC, `auto_upgrade=false`. A healthy active carrier is retained.
- `GOMAXPROCS=4` per TG on a 12-logical-CPU Linux Docker VM. This is not a result
  for a four-vCPU host; kernel WireGuard and other processes also use the VM.
- Two isolated network namespaces on one VM. No real Iran path, DPI, WAN latency,
  10 Gbps capacity, reverse-direction 30-minute result or 24-hour result is claimed.

The independent server receiver JSON matches embedded server output exactly.
Acceptance uses receiver intervals, not client sender bitrate: average and every
complete rolling ten-second window must reach 300 Mbps for at least 1800 seconds.
The minimum full approximately one-second interval was
182.52 Mbps; 12
full one-second intervals were below 300 Mbps. Thus this is a ten-second-window
stability criterion, not an instantaneous guarantee. Inner TCP retransmissions:
12,325.

The last monitor samples before steady load ended recorded these cumulative
counters (sampling may precede the boundary by up to five seconds):

```json
{
  "edge": {
    "dropped_packets": 2,
    "app_errors": 0,
    "malformed_frames": 0,
    "carrier_switches": 0,
    "udp_receive_buffer_errors": 11852,
    "sample_time": 1792618331.576257
  },
  "exit": {
    "dropped_packets": 0,
    "app_errors": 0,
    "malformed_frames": 0,
    "carrier_switches": 0,
    "udp_receive_buffer_errors": 0,
    "sample_time": 1792618330.9456892
  }
}
```

Kernel ingress burst losses remain visible even if the receiver-speed gate passes;
this result does not imply zero packet loss.

## Memory

| TG process | RSS median min 5–10 MiB | RSS median final 5 min MiB | Growth MiB | Peak sampled RSS MiB | RSS slope MiB/min |
|---|---:|---:|---:|---:|---:|
| edge | 16.99 | 17.13 | 0.14 | 18.75 | 0.00360 |
| exit | 18.32 | 18.40 | 0.07 | 25.34 | 0.00200 |

Both RSS and PSS passed the predeclared gates: complete sample coverage, gaps at
most ten seconds, unchanged PID/start ticks, median growth at most 16 MiB and
post-warmup slope at most 0.5 MiB/min. Raw process memory is separate from whole
container memory, which includes iperf JSON history, monitors and charged socket/
kernel memory. This does not prove absence of leaks beyond the observed duration
or measure all kernel WireGuard memory as part of TG.

## Repeated loaded outages

After the steady run, the same processes carried another 180.008068s
of load. Each of three cycles blocked UDP for about six seconds, TCP for about six
seconds, and both for about three seconds. All 24 individual rule mutations and
actual times are retained. All three cycle-ending sets of five full receiver
intervals reached 300 Mbps. Both carriers recovered healthy; daemon PID/start ticks
and the exit application socket stayed unchanged.

During faults, sampled peak RSS was 17.88 MiB at edge and 24.15 MiB at exit.
Final 30-second median RSS was 17.12 / 18.39 MiB, respectively; growth relative
to the last steady minute was +0.07 / -0.30 MiB. These extra observations also
show that the bounded queues settled after repeated outages.

Including intentional cuts, average was 350.02 Mbps and minimum
ten-second window 138.05 Mbps; longest observed consecutive
zero-byte receiver intervals totalled 5.00s.
The ordinary speed gate is not applied across intentional all-path downtime.
Catch-up after restoration does not mean uninterrupted delivery during the cut.

## Diagnosis and validation

The [diagnosis](wireguard-fix.md) retains failed controls and five interrupted
long attempts. Synchronous UDP output, per-frame DATAGRAM overhead, small ingress
receive headroom and per-packet/path oscillation were addressed with bounded
batching, backpressure and stable path selection. A larger independent DATAGRAM
reader still failed a later long attempt. The [reliable QUIC decision](reliable-quic-decision.md)
therefore selects native ordering, retransmission and flow control for the opaque
WireGuard TCP aggregate. It adds head-of-line delay under loss, requiring WAN tests.

Fresh race tests across all six Go packages, `go vet`, eleven Python analyzer tests,
binary/source hash checks, independent receiver comparison and lab cleanup passed.
The original full baseline remains [here](wireguard-validation.md); its average was
199.76 Mbps and minimum ten-second window 74.19 Mbps.

## Reproduction and receipts

With Linux Docker, build `tools/wgsoak/Dockerfile` as `tg-wgsoak:local`, build the
recorded runtime, then run from the repository root:

```sh
python3 tools/wgsoak/run.py --binary /absolute/path/tg-linux-amd64 \
  --out /new/empty-result-path --seconds 1800 --fault-seconds 180 \
  --skip-https-probe --gomaxprocs 4
```

The output directory must not exist. The harness creates only isolated lab
namespaces/resources, configures WireGuard there, and removes private key volumes
and lab containers/networks in cleanup. It does not change host routing or rules.
The delivered local `tg-wireguard-fixed-30min.zip` includes the tested binary,
source archives, raw independent iperf receivers, monitors, actual fault events,
failed controls/interrupted attempts, cleanup, verification logs and checksums.
The tested runtime source is distinguished from the later documentation commit.
