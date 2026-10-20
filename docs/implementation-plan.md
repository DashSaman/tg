# TG v0.1 Implementation Plan

> Execution: direct in this session, explicitly authorized by the owner.
> Skills: writing-plans, executing-plans, test-driven-development.

**Goal:** A runnable, locally verified UDP session tunnel with secure heterogeneous
carriers and transport failure recovery. **Spec:** spec-v0.1.md.
**Stack:** Go >=1.26, quic-go v0.64.1, Python stdlib for iperf3 receipts.

## Constraints and review focus
No invented speed results, remote mutations, secret commits, insecure TLS, or private
t6 source. Dedicated clone on `codex/tg-v0.1`. Test malformed/oversized frames,
replayed/late packets, dead carrier while another is healthy, blocked writes during
shutdown, wrong peer/token, and receiver-only incomplete benchmark output.

### 1. Packet/session primitives
Files: internal/session/{frame.go,reassembly.go,scheduler.go,*_test.go}.
Interfaces: Encode/Decode(Frame); Reassembler.Push(now,Frame); Selector.Choose(now,[]Path).
- [x] Write failing behavioral tests; run and confirm missing behavior.
- [x] Implement bounded frame codec, expiry/dedup and anti-flap selection.
- [x] Run package tests and record result.

### 2. Configuration, TLS and transports
Files: internal/config, internal/pki, internal/transport.
Interfaces: config.Load(path); Config.Validate(); transport.Wire.Send/Receive/Close;
transport.Listen/Dial for HTTPS/2 and QUIC, with authenticated epoch/session hello.
- [x] Write failing tests for strict config, real TLS and rejected auth.
- [x] Implement config-relative paths, PKI, transport deadlines and HTTP cover.
- [x] Run package tests and full suite.

### 3. Runtime session continuity
Files: internal/engine/{engine.go,link.go,app.go,stats.go,*_test.go}.
Interfaces: New(config) -> Engine; Engine.Run(ctx); Engine.Snapshot().
- [x] Write failing real-UDP integration tests for byte integrity and carrier loss.
- [x] Implement stable application socket, peer epochs, health, bounded queues,
  reconnect and metrics. Keep surviving path probes live during recovery.
- [x] Run full suite/race; test changed epoch and graceful cancellation.

### 4. CLI, operator docs and acceptance
Files: cmd/tg, examples, deploy, tools/acceptance.py, tools/test_acceptance.py, CI.
- [x] Test generated config validation and receiver-window acceptance failures.
- [x] Implement init/check/run, generated configs, systemd, bilingual setup and honest status.
- [x] Run tests/vet/builds and inspect full relevant diff.
- [x] Publish the feature branch and [draft PR #1](https://github.com/DashSaman/tg/pull/1).

## Progress
Core implementation and fresh local verification are complete: six Go packages,
21 Go test/subtest cases including fuzz seeds, four Python test cases, race and vet.
Linux amd64/arm64 and Windows amd64 builds passed; two native architecture smoke
runs passed. Published on `codex/tg-v0.1` for review. See validation.md for evidence
and limitations.
The owner explicitly deferred real-host acceptance. It remains mandatory before
claiming production Iran stability or the 300 Mbps aggregate target.
