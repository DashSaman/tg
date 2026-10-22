# Independent sessions for aggregate capacity

The required goal is 10 Gbps aggregate with low CPU. It remains unverified.
Do not equate worker count, configured rate, or a larger receive window with
delivered goodput. The single-VM two-session tests did not achieve this goal.

```sh
tg init --dir private-workers --server-name exit.example.net --redundant --instances 4
tg check --config private-workers/worker-01/edge.json
tg check --config private-workers/worker-01/exit.json
```

`--instances 1` preserves the original flat pair layout. Counts 2..32 create
`worker-01` through `worker-NN`, each with independent PKI, HTTPS token, session
ID, replay log and UDP peer. `workers.json` lists relative config paths and
explicitly sets `capacity_verified:false`. It contains no token or key material.
Initialization refuses an existing directory; failed partial output is preserved.
Linux private directory/file modes remain 0700/0600; apply Windows ACLs separately.

For worker index i starting at zero, generated loopback ports are:

| Endpoint | Port |
|---|---:|
| Edge application | 31820+i |
| Exit application / inner WG listener | 51820+i |
| HTTPS carrier | 8443+2i |
| QUIC stream carrier | 8444+2i |
| Edge metrics | 19090+2i |
| Exit metrics | 19091+2i |

These are local initialization settings, not an Internet deployment. Follow
[operations](operations.md) to set each carrier's remote address/bind address,
verified TLS server name and host-specific credentials. Provision only the
corresponding leaf identity, config, CA certificate and token to each host;
keep every CA private key offline. Never copy the entire generated directory to
both production servers. Run each config as a separately supervised process.

Assign proxy **connections**, or IP client **subnets/flows**, to separate inner
WireGuard interfaces/peers before encryption. Each pair requires its own inner
WG identity, addresses, endpoint port and applicable routing/NAT rules. TG does
not create these interfaces, select users, balance routes or install firewall
rules. Arbitrarily round-robin packets of one encrypted WG peer across workers
breaks the independent UDP peer/session contract. A worker's QUIC/HTTPS paths
still provide redundancy within that worker; independent workers do not pool
the bandwidth of those paths or eliminate common-IP failures.

Start with the smallest worker count that improves **measured** capacity. More
processes can consume more CPU and reduce speed on a shared server. There is no
CPU pinning, busy polling, global sysctl change, or implicit GOMAXPROCS override.
Measure all TG processes plus host kernel load, one global CPU counter per host,
receiver goodput, packet sizes, RTT, loss, backup copies and memory under cuts.

## High-RTT memory budgets

At 10 Gbps and 100ms RTT, payload bandwidth-delay product is about 125MB before
overheads. `retained_packets` remains 8192 by default; explicit values 128..131072
must also satisfy `retained_packets * max_packet_bytes <= 256MiB`. Thus 131072
packets fit a 2048-byte budget, while 8192-byte jumbo budgets allow at most32768.
Ordered output additionally caps retention at16384. This is a sender payload
bound, not a process RSS cap or an end-to-end throughput guarantee. Receive
windows, replay metadata, application queues and Go GC consume additional memory.
Each worker multiplies these budgets. The receiver dedupe history is larger than
every allowed sender count. An explicit large HTTP2/QUIC receive window may also
be needed at high RTT; the local32MiB experiment provided no throughput win.

The current CPU improvement and retained failed capacity probes are documented
in [CPU-efficient capacity work](low-cpu-capacity.md). Jumbo7168-byte inner MTUs
target TCP sessions terminated at the server/proxy endpoints. Forwarded public
IP traffic often retains ordinary end-to-end packet sizes; test it separately.
