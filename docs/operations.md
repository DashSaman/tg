# Operating v0.1

## Topology

```text
Iran users -> inner WireGuard -> TG edge UDP :31820
                         | QUIC / HTTPS2 |
Internet <- overseas WireGuard :51820 <- TG exit stable UDP socket
```

Configure the Iran WireGuard peer's Endpoint as `127.0.0.1:31820`. Overseas WireGuard
listens on UDP 51820; restrict external exposure separately. Authenticated WireGuard
roaming learns the local TG exit source endpoint. TG forwards one inner application
peer pair. Unrelated UDP clients must not share an edge listener: replies go to its
last sender. Many users can be routed through the inner IP tunnel. Configure their
addressing, NAT, keys, AllowedIPs and routes separately; keep outer endpoints outside
the inner default route to avoid a loop.

Start inner MTU testing around 1280, then measure. Default max UDP payload is 2048;
fragment payload is 1024. HTTPS fallback can add TCP head-of-line blocking. Socket
continuity does not recover lost UDP packets; inner TCP/application recovery and
WireGuard handshakes are still needed.

## Provision

Build with Go 1.26+. On a trusted machine run
`tg init --dir lab --server-name YOUR_VERIFIED_NAME`. The directory must not exist.
This generates private lab PKI, not public WebPKI or a stealth mechanism. Leaves
expire after one year. No automatic certificate rotation is shipped.

| Host | Files to provision securely |
|---|---|
| edge / Iran | edge.json, edge.crt, edge.key, ca.crt, web.token |
| exit / overseas | exit.json, server.crt, server.key, ca.crt, web.token |
| offline | ca.key and issuance backups |

On exit change carrier addresses to intended listeners, e.g. `0.0.0.0:8443` TCP for
HTTPS and `0.0.0.0:8444` UDP for QUIC. On edge use the exit's reachable IP/DNS and
matching ports. Keep the certificate `tls.server_name`; verification is mandatory.
Relative paths resolve against config directory. Session ID, HTTP path and token
must agree; session ID/path are not authentication credentials.

Different carrier endpoints must reach the same exit session instance. Both on one
IP share IP-block risk. Native QUIC uses a recognizable ALPN; HTTPS/2 alone does not
prove DPI resistance. No carrier guarantees censorship bypass.

Run `tg check --config FILE`, then `tg run --config FILE`. Check verifies JSON,
bounds and local TLS role/name/expiry/trust/token, not remote connectivity. Changes
require restart. Use owner-only Linux permissions; configure Windows ACLs explicitly.

## Observe and recover

Loopback metrics: edge `127.0.0.1:19090`, exit `127.0.0.1:19091`; empty string disables.
Never expose these unauthenticated diagnostics publicly:

- `/healthz`: 200 with a responding authenticated carrier, otherwise 503.
- `/status`: JSON health, carrier RTT, RTT variation, queue occupancy and counters.
- `/metrics`: Prometheus text. RTT variation is EWMA absolute deviation from smoothed
  carrier RTT, not RFC3550 application-packet jitter. Counters reset on restart.

`app_queued_bytes` means queue admission, not remote delivery; `app_output_bytes`
means handed to a local UDP socket, not Internet goodput. Measure final application
receivers. Full Internet-exit health needs a separate application probe, not shipped.

Defaults: probes every 1s, expiry after 4s, independent reconnect backoff up to about
3s. Healthy upgrades need two fresh observations, 10ms score advantage and 5s hold.
Score: RTT + twice RTT variation + configured cost + bounded queue penalty. HTTPS
costs 20ms by default. These are test starting points, not validated Iran tuning.
Queue pressure can try another healthy carrier immediately. Failure detection can
drop UDP; lossless handover and TG-level application retransmission are not provided.

New configurations use `queue_frames: 1024` per carrier. Existing explicit values
are preserved. With two frames per 1400-byte application packet at 350 Mbps, this
is roughly 16ms of burst headroom, not a latency guarantee. The application UDP
socket requests a 1MiB receive buffer; OS limits can clamp it. Increase queues only
with receiver loss and latency measurements: longer queues retain more memory and
may increase latency on slow paths. See [local performance evidence](performance.md).

TG never restarts remote daemons or changes kernel networking. Recovery backoff does
not stop surviving probes. All-path failure drops new packets instead of accumulating
backlog. Exit restart replaces the app socket; continuity covers carrier reconnects
and edge restarts while exit remains alive.

## Service

`deploy/tg@.service` assumes an existing unprivileged `tg` user, executable
`/usr/local/bin/tg`, and `/etc/tg/{edge,exit}.json` plus private credentials readable
by that user. It is a template, not an installer. TG on high ports needs no root or
CAP_NET_ADMIN. WireGuard, open ports and user routes are separate deployment tasks.
