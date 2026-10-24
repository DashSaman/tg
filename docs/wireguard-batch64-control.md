# Full 64-frame independent-output control: weighted speed FAIL

Clean runtime `8561ec4423243f33b0496bd03c74024cee39361d` completed 1800 seconds
steady, 180 seconds single-path cuts and 180 seconds mixed/all-path cuts. The
profile matched the previous 350 Mbps offered aggregate, eight TCP streams,
128 KiB writes, WG MTU 1280, HTB/FQ-CoDel at 400 Mbps, local UDP cap 500 Mbps,
HTTPS preferred and QUIC backup with 10 ms deferred copies. Output was independent.

Steady average was 350.0007 Mbps. Its 1798 approximately one-second intervals had
a minimum of 330.146 Mbps and no normal interval below 300. However, the interval
649.000081–650.084291 seconds lasted 1.084210 seconds and received only 18,593,680
bytes: 137.196 Mbps. The next interval lasted 0.915725 seconds. The weighted
one-second floor FAILED. The long interval's byte total itself rules out treating
this as only a harmless timestamp adjustment; the low-rate interval stays visible.

Single-path cuts passed: minimum 328.726 Mbps, no bad normal/weighted windows.
Mixed cuts kept their expected zero-throughput windows and recovered in all three
cycles. PID/socket continuity and TG RSS/PSS checks passed. RSS median growth was
−0.340 MiB edge / +0.100 MiB exit. The 1500 loaded ICMP probes all returned:
median 1.27 ms, p95 7.38 ms, maximum about 426 ms, RTT population deviation
11.414 ms. Median/p95/zero-loss targets passed; the large tail spike is retained.

Independent receiver evidence, source/binary identity, fresh race/vet/31 Python
checks and lab cleanup were verified. The independent analyzer additionally
records incomplete normal-interval resolution and keeps the overall failed verdict.
The report plots irregular interval averages in orange and shows both the normal
interval minimum and weighted minimum, so the normal-only plot cannot hide the dip.
Raw receipts and reports remain in the local `tg-batch64-350-30min` evidence folder.

The subsequent [application duplex-lock regression](application-duplex-lock.md)
identifies a concrete cross-direction blocking mechanism. Its unit-test fix must
be measured in a new run; this failed full control is not relabeled as accepted.
