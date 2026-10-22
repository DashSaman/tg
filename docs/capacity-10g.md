# Required aggregate capacity: 10 Gbps

**10 Gbps aggregate is a required project goal and has NOT been achieved or
validated.** The 300 Mbps floor is the first milestone. The successful
[350 Mbps offered-load profile](wireguard-qos-validation.md) does not complete
the full requirement. Neither accepting a 10000 Mbps configuration value nor
adding nominal capacities of untested workers demonstrates 10 Gbps delivery.

## New capacity probes

The same clean `bba3467e17a1ff29e37ae1214de8073b886fe614` Linux amd64 binary
was measured on the shared local WSL2 Docker VM, kernel WireGuard MTU 1280,
eight TCP streams, GOMAXPROCS=4 per TG process. The previous 400 Mbps WG QoS and
500 Mbps TG output cap were both disabled. These are short diagnostic probes,
not 30-minute stability or WAN acceptance.

| Path | Offered aggregate Mbps | Duration | Receiver average Mbps | Minimum full ~1s Mbps |
|---|---:|---:|---:|---:|
| Direct kernel WireGuard, TG bypassed | 10000 | 60s | 649.21 | 390.32 |
| TG redundant HTTPS/2 + QUIC-stream, HTTPS preferred | 10000 | 60s | 949.04 | 187.89 |
| Direct kernel WireGuard, TG bypassed | Unpaced | 60s | 623.58 | 350.21 |
| TG, explicit 10000 Mbps receiver target | 10000 | 30s | 1267.14 | 1036.88 |

All four results fail a 10000 Mbps capacity target. The older harness's native
`passed:true` in the direct probe receipts referred to its **300 Mbps** default
floor, not a 10 Gbps result. TG's probe also had two complete seconds below 300.
The separate accepted paced profile retains its original result and artifacts.
All disposable resources from these probes were removed.
[Compact source-identified evidence](capacity-10g-results.json) records all four
negative capacity verdicts and hashes of their raw receipts. The fourth probe
exercises the new explicit target through the actual CLI and real WG/TG path.
Its shorter duration does not establish an improvement over the earlier probe.

The direct controls also fall far below 10 Gbps, but do not establish a physical
hardware limit: different scheduling, generator behavior, TCP windows and
offload paths can affect these measurements. TG even exceeded the matched
direct average. This is not evidence that TG improves native capacity, nor
proof of one sole bottleneck. No 10 Gbps claim is justified by this environment.

## Target-aware acceptance

`tools/wgsoak/run.py --target-mbps` now selects the receiver floor independently
of the generator's `--mbps`. It is recorded in the manifest and per-phase
receipts and applies to both ordinary speed and post-outage recovery. The
default remains 300 for the first milestone. A synthetic 350 Mbps receipt must
fail a 10000 Mbps ordinary/recovery target; tests also check positive high-rate
receipts and invalid targets.

An example **future** high-capacity acceptance command is:

```sh
python tools/wgsoak/run.py --binary /absolute/path/tg-linux-amd64 --out /absolute/path/new-results --seconds 1800 --fault-seconds 180 --skip-https-probe --gomaxprocs 4 --redundant --window 1 --fault-plan single-path --extra-mixed-faults --udp-mode quic-stream --mbps 12000 --target-mbps 10000 --app-pacing-mbps 0 --wg-qos-mbps 0 --preferred-carrier https
```

This command is a test request, not a supported-capacity declaration. Preserved
failures must stay failed. Real deployments need measured queue/rate settings;
the diagnostic removal of caps is not a production tuning recommendation.

## Engineering still required

1. Establish a direct kernel-WireGuard and traffic-generator baseline above the
   intended rate with CPU, socket, NIC and offload observations. Separate
   generator limitations from tunnel limitations before sizing workers.
2. Profile the existing read/decode/reassembly/write path at higher packet rates.
   The current pipeline has one application input reader and one output worker
   per TG process; increasing a configured cap does not parallelize them.
3. Size replay and carrier windows for actual RTT with explicit memory bounds.
   The 8192-packet log holds about 10.7 MB at 1312-byte WG ciphertext packets:
   only about 8.6ms at 10 Gbps. At 100ms the raw bandwidth-delay product alone
   is 125 MB; the QUIC 8 MiB stream window also needs separate examination.
4. Compare a larger bounded pipeline against independent session/flow shards.
   Independent WG/TG pairs can spread crypto and session work, but need explicit
   user/flow assignment, routing, resource budgets and failure recovery. Such
   sharding is **not implemented** by the current single-session prototype.
5. Validate aggregate receiver goodput, memory and loaded latency for 30 minutes,
   then repeated single-path cuts. Every surviving path must have the required
   measured capacity. All-path blocking necessarily interrupts delivery.

Iran WAN/DPI behavior, high RTT, reverse/symmetric capacity and 24-hour behavior
remain separate validation requirements. The project is not complete merely
because the first local 300 Mbps milestone passed.
