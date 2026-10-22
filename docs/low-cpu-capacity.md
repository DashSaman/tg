# CPU-efficient capacity work

The required final capacity remains **10 Gbps aggregate**, not yet achieved.
The existing 300 Mbps acceptance is a first milestone. This work measures CPU
seconds per delivered gigabit rather than treating a configured rate or the
addition of worker counts as delivered capacity.

## First bounded change: Linux application UDP GRO

The current application output already uses UDP GSO. The input socket now also
tries `UDP_GRO`, allowing the kernel to deliver multiple original datagrams in
one receive buffer. TG splits only using validated native GRO control metadata;
each original packet still gets an owned copy before asynchronous admission.
Protocol framing, TLS authentication, replay IDs and delivery ACK semantics do
not change. No kernel-bypass transport, host sysctl or privileged TG hook is added.

The Linux batch has 32 fixed 65535-byte buffers (about 2 MiB per application),
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

Performance results are pending. Raising caps or allocating more memory is not
yet an efficiency improvement; retain the change only with measured benefit or
clear capability evidence and document any regression. Independent sessions,
high-RTT window sizing and 10 Gbps hardware/path validation remain unfinished.
