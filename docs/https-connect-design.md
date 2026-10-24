# Optional authenticated CONNECT carrier

TG now accepts the explicit `https-connect` carrier kind alongside `https`
(HTTPS/2), `quic-stream`, and the experimental QUIC DATAGRAM carrier. The
generated default pair still uses HTTPS/2 and QUIC streams. There is no automatic
protocol downgrade or claim of resistance to Iran's filtering from local tests.

CONNECT uses standard TLS1.3 with verified server identity and a verified client
certificate, then HTTP/1.1 CONNECT. Before accepting a session, the server checks
the bearer token in constant time, private path header, session identifier,
nonzero peer epoch, and exact repair/packet-mode identifier and byte ceiling.
The client requires HTTP/1.1 ALPN and the matching successful response. Existing
HTTPS/2 and CONNECT endpoints cannot silently interoperate or bind the same TCP
address. A normal HTTP reverse proxy/CDN needs explicit CONNECT tunnel support;
an HTTP/2-only intermediary is insufficient.

After authentication, the connection carries the existing bounded TG framing,
shared replay log, ACK/deduplication, probes and independent reconnect workers.
The raw TCP reader lazily allocates at most256KiB of ciphertext read-ahead only
after all authentication/session checks. It never waits to fill that buffer.
Writes remain independent of reads. Optional `tls_record_batch` applies the
existing bounded encrypted-record writer. No new cryptography is introduced.

Only CONNECT in negotiated `stream_packets` mode uses the new borrowed receive
batch. It blocks for the first frame, then returns at most32 complete frames
already present in the64KiB plaintext buffer. Partial tails remain for the next
read. Every retained application packet is copied before the next carrier read.
Consecutive packet frames share receive/engine locks; control frames flush the
preceding packet group. A full output queue never holds the engine lock. Old
receiver ownership survives an epoch reset; stale ACKs cannot enter the new
epoch. A valid prefix is processed before a subsequent framing/read error.
Shutdown aborts underlying sockets, including hijacked HTTP connections.

## Enabling an experimental pair

Generate a private pair normally, then edit both generated configurations:

```json
{
  "redundant": true,
  "stream_packets": true,
  "tls_record_batch": true,
  "max_packet_bytes": 2048,
  "retained_packets": 8192
}
```

These are fields to merge, not a complete configuration. Change the TCP
carrier's `kind` from `https` to `https-connect` on both peers; preserve its
generated private path and provisioned credentials. Set its `cost_ms` lower
than the QUIC backup to prefer the cheaper TCP path. Run `tg check` on each
configuration before deployment. Keep the application and metrics sockets on
loopback. The replay payload budget remains256MiB maximum.

For ordinary routed IP, use an appropriate small packet ceiling, such as the
2048-byte lab profile. The60000-byte WireGuard MTU experiments describe proxy
endpoints with a large virtual tunnel MTU, not arbitrary1500-byte Internet
packets. Large packets need an explicit ceiling up to65507 and a smaller replay
count (the lab used2048); they do not establish ordinary-IP8Gbps capacity.

## Measurement and acceptance

Initial immutable lab prototypes replaced HTTPS/2 only in copied source. They
reached6.25Gbps with both paths and7.61Gbps with a buffered TLS reader and an
isolated TCP path. Another two-scheduler-worker TCP run reached7.28Gbps.
These are short, shared-VM, full-WireGuard observations. Every8Gbps gate failed;
none is a clean-build30-minute acceptance or a WAN measurement. The full receipts
and the failed receive-batching A/B/B/A controls are preserved outside source.

The generic receive-batching experiment did not improve throughput consistently,
so existing HTTPS/2 and QUIC receive dispatch stays unchanged. New carrier
measurements must use an exact clean build, matched protocol controls, receiver
goodput, CPU per delivered gigabit, loaded RTT/variation, bounded RSS/PSS, and
loaded TCP/UDP/all-path cuts. The original300Mbps milestone and8/10Gbps goals
remain distinct. Results are recorded separately when those runs complete.
