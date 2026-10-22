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

The last DATAGRAM control also completed 180s mixed cuts and recovered after all
three cycles; its all-path speed minimum was zero and remains a failed speed
verdict. It is not an accepted overall result. The stream control also recovered
after all-path cuts, with ten below-target seconds in that deliberately blocked
phase. A subsequent candidate uses the [shared sender log](shared-sender-log.md)
instead of independent per-carrier packet queues and timed packet replay.

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
