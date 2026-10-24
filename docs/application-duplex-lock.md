# Independent application read and write synchronization

The edge application's input reader updates its last valid local peer under a
mutex. Previously, application writes held that same mutex while setting a write
deadline and entering UDP send/sendmmsg. A backpressured or preempted writer could
therefore hold up input in the opposite direction, even after the input datagram
had already been read from its socket. This couples both directions of the inner
WireGuard transport at an unnecessary synchronization point.

`TestApplicationBackpressuredWriteDoesNotBlockOppositeRead` first holds a controlled
batch writer open, then sends a real opposite-direction UDP input. Before the fix,
the test timed out after one second because the input reader could not update its
peer. With separate writer and peer locks, the same test completes immediately.
It also verifies that an in-flight output batch keeps its original peer snapshot,
while a new valid input can update the peer for future writes.

The writer lock still serializes deadlines, reusable batch buffers and native
socket writes. The peer lock protects only the address snapshot/update. Neither
writer backpressure nor writer-side deadline serialization holds the peer lock.
The wire protocol, credentials, carrier preference and queue limits are unchanged.

All Go race-test packages, vet, and the 31 Python lab/QoS/acceptance tests passed.
The earlier disposable profile had already shown application.writeBatch among
the edge's mutex-wait stacks; the new regression isolates one concrete mechanism.
This proves and fixes cross-direction lock blocking. It does not yet establish
that this mechanism caused every earlier speed dip; fresh full-stack load and
fault tests are required for a new throughput acceptance claim.

The ordered-output diagnostic on runtime 8561 was deliberately interrupted while
this regression was investigated. Its partial receipts are retained and labeled
as interrupted, with empty cleanup results; it is not a completed acceptance run.
