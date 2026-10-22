# Bounded complete-packet mode on reliable carriers

The owner confirmed the next target is1GB/s, approximately8000Mbps aggregate,
while retaining the final10000Mbps goal, low CPU, and both proxy/IP traffic.
Raw TCP delivered about12Gbps in the local path-isolation diagnostic; adding
generators did not improve it. TG still falls short. Native WG MTU1500 produced
outer-IP fragments; the explicit1420 control removes those fragments and still
falls far short of8Gbps. Neither native bypass nor worker count is a capacity fix.

An explicit `stream_packets:true` mode removes redundant fragmentation on already
reliable HTTPS/QUIC streams. The default remains the existing protocol. Complete
packets use a distinct frame kind with original ID, index0/count1, exact length
and at most8192 bytes. A7200-byte ciphertext packet becomes one frame rather
than eight. The receiver checks the same dedupe/contiguous ACK history, takes an
owned bounded buffer, and bypasses per-packet fragment assembly-map allocation.
No crypto, encryption or authentication is removed. Sender replay, ACK ownership,
queue limits, shutdown and carrier recovery retain their existing contracts.

Both peers must opt in: HTTPS mode is distinct and native QUIC uses a distinct
ALPN. Mismatches fail closed. Config validation permits this mode only with
acknowledged redundancy and reliable carriers; DATAGRAM remains unchanged.
Legacy decoders reject the complete-packet kind even if its payload is short.
Packet-mode decoders reject legacy Data fragments. Inconsistent pending assembly
IDs must not be overwritten, and duplicate delivery must not occur.

Stream read/write and transmitter storage have explicit per-frame bounds and
are allocated for the selected mode, not a giant fixed array for legacy users.
There is no fill timer, extra queueing delay, busy polling or host network change.

Implementation sequence: failing session/ownership/config tests; packet codec and
reassembler fast path; mode-bound transport and bounded buffers; log encoding and
engine dispatch; actual TLS/QUIC mismatch/large-packet tests; full race/vet and
lab tests; clean binaries. Then compare the same binary with mode off/on under
the same ordinary and jumbo profiles, retaining every failed8000Mbps verdict.
Compare CPU per delivered Gbit, memory and recovery as well as speed. A short
peak or configured rate is not stable1GB/s acceptance; no speedup is assumed.

For a compatible pair, set `"stream_packets": true` in both TG JSON
configurations, retain `"redundant": true`, and use only `https` and/or
`quic-stream` carriers. Validate each configuration with `tg check --config
<path>` before restarting both peers. Older peers cannot negotiate this mode.
The status JSON exposes `stream_packets` so deployment can verify the running
mode. The lab accepts `--stream-packets` and checks both generated peer configs.
This remains experimental until throughput and loaded recovery measurements
on the exact built revision have been recorded.
