# Redundant session investigation — 2026-10-22

The acceptance target remains >=300 Mbps aggregate receiver goodput in every
complete approximately one-second interval and one-second rolling window, both
at steady load and during a single-carrier cut. An all-carrier cut has a recovery
criterion and retains its actual speed failure. Short controls cannot certify
30-minute memory behavior. No WAN/DPI/10 Gbps/24-hour claim follows from this lab.

Controls use kernel WireGuard, eight TCP streams paced at 350 Mbps aggregate,
MTU 1280, GOMAXPROCS=4 per TG, on a 12-logical-CPU Linux VM. The independent iperf
receiver is authoritative; live /proc I/O deltas are advisory and can differ.

| Control | Complete steady phase | Minimum full 1s Mbps | Result |
|---|---:|---:|---|
| First clean redundant attempt b2683aa | Interrupted at 207s | 174.69 | Failed; not 30 minutes |
| Adaptive RTO/backoff alone | Interrupted at 211s | 219.35 | Failed |
| Restore input backpressure, stream | 300s | 161.92 | Failed |
| DATAGRAM comparison, strict output | 300s | 250.13 | Failed |
| Direct kernel WireGuard, TG bypassed | 300s | 321.17 | Direct baseline pass, not TG acceptance |
| Data RTO without early gap repair | Interrupted at 228s | 39.33 | Failed |
| Deferred first-send queue correction | 300s | 59.66 | Failed |
| Instrumented CPU/mutex/block probe | 330s | 35.68 | Diagnostic only |
| Independent UDP output | 300s | 173.32 | Failed; single-path minimum 261.54 |
| Actual queue admission before numbering | Interrupted at 221s | 214.69 | Failed |
| Fast repair limited to oldest missing span, DATAGRAM | 300s | 210.35 | Steady failed; 180s single-path passed at 332.66 minimum |
| Same binary, QUIC-stream | 300s | 276.95 | Steady failed; 180s single-path passed at 323.25 minimum |
| Shared sender log, reliable carriers, 350 Mbps offered | 300s | 298.51 | Steady failed: one full one-second interval below 300; no TG input drops |
| Same full-copy shared log, 400 Mbps offered | 300s | 271.14 | Steady failed; single-path minimum 216.61; edge app socket dropped 1298 packets during steady load |
| 10ms deferred backup copies, 400 Mbps offered | 300s | 315.00 | Steady short pass; 180s single-path phase failed at 220.40 minimum |
| Same binary/load, GOMAXPROCS=8 | 300s | 215.42 | Steady failed (6 bad seconds); 180s single-path phase minimum 286.84, one bad second |
| 10ms backup grace, GOMAXPROCS=4, 16 KiB TCP writes | 300s | 280.05 | One bad steady second; 180s single-path phase passed at 389.21 minimum |
| Local UDP cap 500 Mbps, original 128 KiB writes, 400 Mbps offered | 300s | 271.84 | One bad steady second; 180s single-path phase passed at 353.56 minimum |
| Clean c9f634f unpaced bulk, local UDP cap 500 Mbps | 1800s | 113.65 | Complete steady failed (19 bad seconds); single-path minimum 397.79 passed; memory passed; loaded ping mean 209.45 ms |
| Same c9 Go binary, pre-encryption HTB400/FQ-CoDel | 300s | 316.01 | Short steady/single-path passed (fault minimum 335.75); loaded ping mean 1.11 ms, p95 1.43 ms; full test pending |

The last DATAGRAM control also completed 180s mixed cuts and recovered after all
three cycles; its all-path speed minimum was zero and remains a failed speed
verdict. It is not an accepted overall result. The stream control also recovered
after all-path cuts, with ten below-target seconds in that deliberately blocked
phase. A subsequent candidate uses the [shared sender log](shared-sender-log.md)
instead of independent per-carrier packet queues and timed packet replay.
The latest controlled variable is a 10ms deferred backup-copy grace, preserving
the same 400 Mbps offered load and unchanged receiver acceptance floor.
That control's seven below-target fault-phase seconds occurred at seconds 60..67,
between planned carrier cuts, with exit's completed-output queue full, sender ACK
turnaround about 300ms and 795 kernel receive drops by the end. Other timed cuts
had no below-target full seconds. A WireGuard handshake changed near the event;
temporal proximity does not prove rekeying caused it. The next comparison changes
only GOMAXPROCS from 4 to 8 per TG, on the same 12-logical-CPU VM, and also records
namespace WireGuard device errors, CPU pressure and cgroup CPU statistics.
The 8-processor comparison also failed. The steady bad samples coincided with
CPU `some` pressure rising from about 13% to 44%, sender retention pressure and
7763 kernel receive drops by the late bad event; cgroup CPU throttling and WG
device errors remained zero. This does not prove which host/kernel/runtime
component caused the scheduling pressure. The next comparison keeps GOMAXPROCS=4
and 400 Mbps, changing only iperf TCP write size from 128 KiB to 16 KiB to test
burst sensitivity. No accepted floor is lowered, and the larger-write failures
must remain visible even if this smoother workload passes.
The smaller-write control still failed overall. A new candidate adds optional
local application output shaping and restores the original 128 KiB generator
writes, with a 500 Mbps UDP output cap and 400 Mbps offered application rate.
Its receiver speed verdict is pending; the cap is not a lowered acceptance floor.
That capped control also failed overall. Its loaded single-path phase passed;
the next complete 30-minute capacity measurement retains the cap and original
write size but uses ordinary unpaced bulk TCP. This is a distinct workload, not a
retroactive pass for the paced controls. All original one-second receiver gates,
loaded fault gates and full memory criteria remain unchanged. The complete run
must finish memory/recovery collection even if speed fails.

Deterministic regression tests separately established and repaired:

- Sequence history could reject a unique delayed copy after 4096 newer completions.
- A sole queued copy could disappear on carrier failure without retained replay.
- Scanning beyond an in-flight prefix amplified duplicate traffic.
- A 128-ID feedback pipeline stalled behind the first in-flight repair; selective
  completion now covers 1024 IDs, with 128 sends per burst.
- Probe RTT omitted data queue delay; repeated repairs lacked backoff.
- Never-admitted packets could overtake ordinary data through priority repair.
- Admission could number a packet before any whole carrier copy entered a queue.
- A strict output sequence barrier delayed independent datagrams behind one hole.
- A final coalesced ACK could remain pending after input stopped.

These are test-backed correctness/scheduling findings. Their fixes alone did not
prove the speed target; negative controls above preserve that distinction.

The disposable profiling copy recorded 300s CPU/mutex/block/heap profiles. CPU
samples corresponded to about 1.55 cores at edge and 1.79 at exit on average;
syscalls accounted for about 56.6%/52.6% of flat CPU samples. Mutex/block totals
aggregate goroutines and are not wall-clock percentages or proof of one cause.
Instrumentation results are not release speed or memory certification.

Retransmission sampling/backoff uses principles discussed in
[RFC 6298](https://www.rfc-editor.org/rfc/rfc6298.html); TG is not a TCP
implementation or a claim of that RFC's timer conformance. QUIC DATAGRAM remains
encrypted and congestion-controlled with different reliability/throughput
properties from streams; see the [quic-go DATAGRAM documentation](https://quic-go.net/docs/quic/datagrams/).
