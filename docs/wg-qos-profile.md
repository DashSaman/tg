# Optional pre-encryption queue control

**Local acceptance passed with 350Mbps aggregate TCP offered rate; real-host acceptance pending.**
See the [current complete result](wireguard-qos-validation.md).
The [earlier full queue-control run](wireguard-qos-control.md) failed its speed floor.
Before queue control, the [shared-log bulk control](wireguard-c9-control.md)
had about 209ms local loaded RTT and failed its complete one-second speed floor.
This profile moves rate/queue control
to the WireGuard IP interface, before users' packets become opaque ciphertext.

Combine the existing original TG session layer with:

- Reliable QUIC-stream and HTTPS/2, acknowledged bounded shared sender log,
  independent reconnect cursors,10ms deferred backup copies and batched UDP.
- Optional local TG UDP output cap500Mbps (ciphertext, with overhead headroom).
- Explicit WireGuard interface HTB rate/ceil400Mbps and FQ-CoDel, target5ms,
  interval100ms, packet limit1024, queue memory limit4MiB, ECN enabled.

HTB supplies the IP rate constraint; FQ-CoDel supplies per-flow fair queues and
active queue management. The rate must fit the real surviving path; it does not
discover an unknown WAN bottleneck. See the upstream iproute2 manuals for
[HTB](https://man7.org/linux/man-pages/man8/tc-htb.8.html) and
[FQ-CoDel](https://man7.org/linux/man-pages/man8/tc-fq_codel.8.html).

The short real-kernel control used the **same c9f634f Go binary** as the failed
bulk control, eight unpaced TCP streams and128KiB writes.300s steady averaged
382.58Mbps with minimum full1s316.01;180s repeated single-path cuts had minimum
335.75. No full1s interval was below300.60 loaded ICMP probes had no loss,
mean1.111ms, median0.914ms, p951.43ms, max9.6ms. TG RSS was about20–22MiB.
Both kernel qdiscs were verified to actually process packets. The complete clean-build control subsequently failed the every-second speed
floor; the linked full result supersedes this short speed pass.

The current complete profile retains the unchanged hard 300 Mbps receiver floor
for steady/single-path phases, recovery after all-path blocking, full RSS/PSS
criteria and stable process/socket checks. It also requires a loaded ICMP sample
with zero loss, median<20ms and p95<30ms in this local lab. Report actual maximum,
sample count and duration; none of these are Iran/WAN latency guarantees.

## Explicit setup

TG itself remains an unprivileged UDP forwarder. `tools/qos.py` is a separate
optional administrative utility, requiring Python3, iproute2 and a kernel with
HTB/FQ-CoDel. Apply only to the intended WireGuard interface, preferably during
initial setup before client traffic starts. The utility verifies interface type,
refuses an existing custom root qdisc, uses project handles7a00:/7a10:, and
rolls back only its newly installed owned root on a partial failure. It never
changes routes, global sysctls, firewall rules or physical-interface queues.

```sh
python3 tools/qos.py plan --interface wg0 --mbps 400
sudo python3 tools/qos.py apply --interface wg0 --mbps 400
python3 tools/qos.py check --interface wg0 --mbps 400
```

Disable explicitly with `sudo python3 tools/qos.py remove --interface wg0`.
Removal refuses an unowned root. Deleting the WireGuard interface also removes
its qdisc. There is no automatic privileged hook in the TG service.

Set both TG configs to `redundant:true`, `ordered_output:false`,
`replica_delay_ms:10`, `app_pacing_mbps:500`, `queue_frames:1024`,
`max_packet_bytes:2048`, and use `GOMAXPROCS=4` for this measured lab profile.
Keep distinct host credentials; this overlay is not a complete config or an
Internet exit installer. WG MTU1280 is the current lab setting.

The lab option `--wg-qos-mbps 400` applies only inside its two disposable network
namespaces. Preflight verifies actual rate/ceil, queue settings and both peers;
postflight requires real packet counters on both root and leaf, so silently
ignored shaping cannot pass. Some older iproute2 versions print class information
as text despite `-j`; the helper verifies the specific class and tolerates at
most0.1% display rounding. Raw class text and JSON qdisc counters are preserved.

This rate trades peak bulk capacity for queue headroom. AQM can deliberately
drop or ECN-mark congestion traffic; zero ICMP loss is not a claim that every
data packet is lossless. Shared-IP blocking, high-RTT window limits, all-path
blackholes, crashes,10Gbps and24-hour behavior retain their separate limits.

## Current tested configuration

Keep each generated host's own TLS identity, token, application address and
carrier endpoints. Set these existing TG fields on **both** peers:

| Field | Tested value |
|---|---|
| redundant | true |
| ordered_output | false |
| replica_delay_ms | 10 |
| app_pacing_mbps | 500 |
| queue_frames | 1024 |
| max_packet_bytes | 2048 |
| HTTPS carrier cost_ms | 0 |
| QUIC-stream carrier cost_ms | 20 |
| WG MTU | 1280 |
| GOMAXPROCS per TG process | 4 |

The generated defaults prefer UDP; explicitly setting these two costs is needed
to reproduce the tested HTTPS preference. Both carriers remain enabled. Use the
explicit WireGuard-only QoS apply/check commands above at 400 Mbps, with the
interface name actually used on each host. The utility refuses a physical NIC
or an existing unowned custom root qdisc; TG itself does not run privileged hooks.

The **350Mbps aggregate TCP offered rate** setting belongs to the benchmark generator. These TG and
400 Mbps qdisc settings do not impose a 350 Mbps production admission limit. The
retained unpaced saturation controls failed, so the observed floor must be read
with its load condition. Real-host capacity, higher RTT and user admission policy
need independent measurement. Internal 64-frame vectors improve batching while
keeping the DATAGRAM wire bundle at 16 frames and preserving per-frame formats.
