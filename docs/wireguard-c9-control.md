# Complete shared-log bulk control, c9f634f — not accepted

The complete local kernel-WireGuard control ran 1800s steady +180s single-path
cuts +180s mixed cuts without restarting TG. It used eight unpaced TCP streams,
128 KiB writes, WG MTU1280, QUIC-stream/HTTPS2 redundancy,10ms backup grace,
500Mbps local UDP output cap and GOMAXPROCS4 per TG on a12-logical-CPU Docker VM.
There was no pre-encryption WireGuard qdisc shaping in this control.

**Overall speed acceptance failed.** Steady receiver goodput averaged458.54Mbps,
but its minimum complete one-second interval was113.65Mbps, with19 of1800 full
seconds below300. Single-path cuts passed at397.79Mbps minimum. All three mixed
cycles recovered; deliberately blocking both carriers retained a zero speed
minimum and11 below-target seconds. That phase has a recovery criterion.

TG RSS/PSS memory criteria passed. Early/late RSS medians were46.883/47.496MiB
at edge (+0.613) and33.543/33.617MiB at exit (+0.074). RSS slopes after warmup
were0.032/-0.002MiB per minute. Processes and application sockets remained stable;
18 WireGuard handshake timestamps were observed per role. Fault-phase resident
memory settled below the steady ending median. This is not a24-hour leak proof.

Latency was unsuitable for the low-latency goal:60 ICMP probes at1Hz under bulk
load had60 replies, min171.674/avg209.452/max232.972/mdev12.170ms. Saturation filled
the bounded sender window; bounded memory alone did not prevent buffering delay.
Kernel interface error counters cannot exclude every internal WireGuard drop.

The independent receiver, memory and source/binary evidence was recomputed.
Fresh Go race tests in all six packages, go vet and5+14 Python tests passed;
verification explicitly records `performance_analysis_passed: false`. Cleanup
removed the two containers, network and temporary credential volume. Native
CI [39709739287](https://github.com/DashSaman/tg/actions/runs/39709739287) passed.

Tested source: `c9f634f5d070bd3153c614bc0632428ce01b8040`, clean VCS stamp.
Linux amd64 SHA256: `d26b90e8ccc3ae759c945918b77cbcc49ea4b0eea3f767f9f03558556b35a3ed`.
Full local receipts are retained in `outputs/tg-resilient-30min` in the task
workspace; [recomputed results](wireguard-c9-control-results.json) are public.
Later profiles must preserve this negative control and cannot replace its verdict.
