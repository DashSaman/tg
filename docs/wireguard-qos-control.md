# Complete pre-encryption queue-control control: speed FAIL

Runtime05ffdaed112eb0c2fcbfc5509cdd19278d2d8514 completed 1800s steady,
180s repeated single-path cuts and 180s mixed cuts including all-path outages.
The exact uninstrumented Linux amd64 binary and all raw receipts are retained
in the local tg-qos-30min evidence directory. [Compact result](wireguard-qos-control-results.json).

Steady average378.736Mbps, minimum full1s126.953Mbps,36 intervals below300.
Single-path cuts average377.251Mbps,min276.954Mbps,two below300; final-five
recovery speed criterion also failed. Mixed-phase speed retained its expected
zero windows during all-path outages; all three cycles recovered above300.
This control did NOT establish a sustained every-second300Mbps floor.

Memory passed: TG early-to-late RSS median growth0.191MiB edge/0.156MiB exit;
late medians21.891/22.297MiB; stable PIDs and application sockets,19 observed
WireGuard handshake timestamps. All1500 loaded pings returned: median1.04ms,
mean2.540ms,p959.48ms,max107ms. Latency passed the prospective20/30ms median/p95
targets. AQM intentionally drops/marks congestion; zero ICMP loss is not a
claim that all data was lossless. All private lab resources were removed.

Profile: eight unpaced TCP streams,128KiB writes,kernelWG MTU1280,
GOMAXPROCS4 per TG on a shared12-logical-CPU WSL2 VM; QUIC-stream+HTTPS2,
shared sender log,10ms deferred backups,500Mbps local UDP pacing; actual
WG HTB400Mbps+FQ-CoDel5/100ms,1024packets,4MiB verified at both peers.
Fresh Go race tests/vet and Python acceptance/QoS/schedule tests passed;
source/binary identity was verified independently, with performance=false.

A subsequent disposable CPU/mutex/block profile reproduced the speed failure.
At the exit,55.29% CPU samples were in syscall execution and28.44% cumulative
in application.writeBatch (27.31% in sendmmsg); AES-GCM decryption was1.20% flat.
This identifies kernel UDP work as a substantial cost, not proof that it is the
sole cause of every drop. Host CPU waiting increased during several dips and
the completed output queue reached256. UDP receive-error counters did not grow.

The next scoped optimization tests Linux UDP_SEGMENT on already available
same-destination application batches. Unlike sendmmsg alone, it can amortize
part of the kernel network-stack traversal. It preserves individual datagrams
and retries only the unsent suffix without offload on unsupported kernels.
Actual effect must be measured; this is not a throughput acceptance claim.
References: [Linux UDP socket semantics](https://man7.org/linux/man-pages/man7/udp.7.html),
[Tailscale's explanation of UDP offload](https://tailscale.com/blog/more-throughput).
No third-party implementation code was copied.
