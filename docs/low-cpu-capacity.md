# CPU-efficient capacity work

The required final capacity remains **10 Gbps aggregate**, not yet achieved.
The existing 300 Mbps acceptance is a first milestone. This work measures CPU
seconds per delivered gigabit rather than treating a configured rate or the
addition of worker counts as delivered capacity.

## First bounded change: Linux application UDP GRO

The current application output already uses UDP GSO. The input socket now also
supports opt-in `app_udp_gro:true`, allowing the kernel to deliver multiple original datagrams in
one receive buffer. TG splits only using validated native GRO control metadata;
each original packet still gets an owned copy before asynchronous admission.
Protocol framing, TLS authentication, replay IDs and delivery ACK semantics do
not change. No kernel-bypass transport, host sysctl or privileged TG hook is added.

When opted in, the Linux batch has 32 fixed 65535-byte buffers (about 2 MiB per application),
plus fixed control storage. Unsupported socket setup keeps the ordinary
32*(max_packet_bytes+1) layout. There is no coalescing timer or busy polling.
Packet/control truncation, malformed/duplicate control and oversized segment
sizes reject the whole bundle before any peer update. Partial batch errors are
returned only after the batch's valid messages have been consumed.

Tests exercise actual IPv4 and IPv6 UDP_SEGMENT -> UDP_GRO socket delivery,
including a shorter final segment, one observed bundle containing nine packets,
and retention of every owned packet across subsequent reads. The original
socket test failed because GRO was disabled. A negative native-int control test
also caught an initial two-byte parser; Linux GRO metadata is a native 32-bit int.
The corrected parser and all-package Go race tests/vet pass.

Kernel contracts: [UDP manual](https://man7.org/linux/man-pages/man7/udp.7.html),
[Linux UDP receive control](https://github.com/torvalds/linux/blob/v6.18/include/linux/udp.h),
[upstream GRO receive self-test](https://github.com/torvalds/linux/blob/v6.18/tools/testing/selftests/net/udpgso_bench_rx.c).

## Measurement plan

1. Preserve the original accepted binary and data. Record a disposable CPU,
   mutex and heap profile to locate actual cost; never package the instrumented
   binary as release evidence.
2. Compare old and new uninstrumented binaries serially on the same eight-stream
   real kernel-WireGuard path, with the same offered load and both profile rate
   caps disabled. Use an order-balanced comparison before claiming a CPU gain.
3. Derive TG CPU seconds from `/proc/PID/stat` and receiver bits from independent
   server intervals covering the same sample boundaries. Report per-role and
   combined CPU seconds/Gbit, actual goodput, drops, replica bytes and memory.
   Kernel/iperf CPU and shared-host pressure are separate observations.
4. Confirm GRO packet counters are positive on the actual WG path. A successful
   synthetic socket test alone does not show the real load used the offload.
5. Recheck speed and path-outage recovery under load. Keep failed windows and
   bounded-memory observations explicit. Short tests are not 30-minute or WAN
   acceptance and cannot establish the required 10 Gbps result.

The first four 120s order-balanced runs at 800 Mbps delivered about 800 Mbps each.
Combined CPU seconds/Gbit were 4.5682 for the old binary and 4.4803 for the GRO
candidate, a 1.92% difference. **Actual GRO bundle/packet counters were zero on
both kernel-WG roles.** This is not an attributed GRO CPU benefit: replica counts
and the two old-binary runs also varied. GRO is consequently off by default;
the synthetic socket capability remains available for paths that really coalesce.
Full receiver minima, including 314.67 and 404.56 Mbps dips, remain recorded.

## Second bounded change: stream read-ahead

The old stream receiver made 128 underlying reads to decode 64 adjacent frames
(a two-byte length read followed by a payload read for each). In HTTP/2 these
small body reads also notify the connection's flow-control/serve loop. CPU
profiles showed substantial cost in these read and server-loop paths.
The receiver now uses one bounded 64 KiB read-ahead buffer per reliable carrier,
shared by HTTPS and native QUIC-stream framing. A real reader is asked only for
currently available bytes; it does not wait to fill the buffer or add a timer.
The regression checks at most two underlying reads for the same 64 frames;
additional tests cover immediate small frames and shutdown of a blocked read.
Frame validation, borrowed-buffer lifetime and underlying close behavior remain.
The completed four-run ABBA comparison at 800 Mbps measured **4.5673 old vs
3.7409 buffered TG CPU seconds/Gbit**, an **18.10% reduction** in this local
diagnostic. Both buffered runs were below both old runs. Average goodputs were
794.32/800.00/800.01/800.01 Mbps in old/new/new/old order; all four complete
receiver speed checks passed 300 Mbps. The new full-one-second minima were
796.88 and 458.11 Mbps; these dips are retained. GRO was off in all four runs.
This measures TG processes, not all kernel WireGuard or physical-host CPU;
two runs per binary on one shared VM are not a universal efficiency guarantee.

The lab also exposes the existing bounded `replica_delay_ms` as an explicit
`--replica-delay-ms` parameter, with both actual peers checked. This permits a
separate 10ms/50ms comparison; a larger grace can reduce duplicate work but
delays backup copies when a primary path blackholes. It is not a speed limit.

## Separate jumbo-inner-packet experiment

The lab now exposes `--max-packet-bytes` and validates that inner WG MTU plus
padding/authentication overhead fits it, before creating lab resources. Both
actual TG peers and kernel WG MTUs are checked. The existing data path supports
8192-byte original packets; a 7168-byte inner MTU produces at most 7200-byte WG
ciphertext datagrams, still fragmented over ordinary outer reliable streams.
An actual IPv4/IPv6 socket test preserves all 32 such jumbo datagrams through
the bounded GSO writer. This does not require increasing the physical WAN MTU.

This experiment targets TCP sessions terminated at the two WG/proxy endpoints.
Forwarded public-IP flows can retain roughly 1500-byte end-to-end packet sizes,
so an improvement here cannot be generalized to arbitrary user traffic. It is
not a recommendation to send jumbo native WireGuard UDP packets over an ordinary
WAN. Explicit capacity tests and further performance results remain pending.
New lab monitoring also records shared-VM CPU ticks (once per host in analysis),
including kernel work and unrelated workloads, separately from each TG process.

The first 60s jumbo probe with 12000 Mbps offered load and an explicit 10000 Mbps
receiver floor received **2452.65 Mbps average / 1954.73 Mbps minimum full 1s**.
It failed the 10 Gbps target in all 60 intervals. TG CPU averaged about 2.62/2.65
cores in an interior monitor span; shared-VM busy CPU was about 7.94 cores total
(not the sum of two identical host observations). This is a short capacity probe,
not a causal comparison against the different earlier MTU/load/duration controls.

The internal stream batch now admits up to 256 existing frames, enough for a
32-datagram jumbo vector (eight fragments per 7200-byte packet). A test rejected
this vector at the old 64-frame bound and now verifies one bounded write/flush
with unchanged frame boundaries. Carrier encode/write storage is about 261 KiB
per buffer; no extra batching timer is added and the DATAGRAM wire bundle stays
at 16 frames. Performance for this additional change is pending.

Raising caps or allocating more memory is not itself an efficiency improvement.
Independent sessions,
high-RTT window sizing and 10 Gbps hardware/path validation remain unfinished.
