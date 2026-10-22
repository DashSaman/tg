# Optional pre-encryption queue control

**Development profile: full speed acceptance FAILED; memory and latency passed.**
See the [complete control](wireguard-qos-control.md). The shared-log
control's bounded queues still caused about209ms local loaded RTT and failed
the complete one-second speed floor. The new profile moves rate/queue control
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

For the next complete profile, retain the unchanged hard300Mbps receiver floor
for steady/single-path phases, recovery after all-path blocking, full RSS/PSS
criteria and stable process/socket checks. Also require a loaded ICMP sample
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
