"""Validate ordinary (non-streaming) iperf3 receiver JSON; never infer sender goodput."""
import argparse
import bisect
import json
import math
import sys


class InvalidReceipt(ValueError):
    pass


def number(value, field):
    if isinstance(value, bool) or not isinstance(value, (int, float)) or not math.isfinite(value) or value < 0:
        raise InvalidReceipt(f"invalid {field}")
    return value


def receiver(obj):
    if not isinstance(obj, dict) or obj.get("sender") is not False:
        raise InvalidReceipt("explicit receiver statistics required (sender=false)")
    return obj


def analyze(document, *, target=300, duration=1800, window=10, max_loss=1, max_jitter=20):
    for name, value in (("target", target), ("duration", duration), ("window", window)):
        if number(value, name) <= 0:
            raise InvalidReceipt(f"{name} must be positive")
    number(max_loss, "max_loss"); number(max_jitter, "max_jitter")
    try:
        # Forward client intervals are sender-local. Use the embedded server's
        # receiver output, or supply the receiver JSON directly (e.g. reverse).
        direct = document.get("intervals", [])
        local_receiver = bool(direct) and direct[0].get("sum", {}).get("sender") is False
        root = document if local_receiver else document.get("server_output_json", document)
        if not isinstance(root, dict) or root.get("error"):
            raise InvalidReceipt("iperf3 error or invalid root")
        start = root["start"]["test_start"]
        if start.get("bidir"):
            raise InvalidReceipt("analyze each direction separately; bidirectional JSON unsupported")
        protocol = start["protocol"]
        if protocol not in ("TCP", "UDP"):
            raise InvalidReceipt("TCP or UDP receipt required")
        end = root["end"]
        summary = receiver(end["sum_received"] if "sum_received" in end else end["sum"])
        summary_seconds = number(summary["seconds"], "summary duration")
        summary_bytes = number(summary["bytes"], "summary bytes")
        samples = []
        losses, jitters = [], []
        for interval in root["intervals"]:
            s = receiver(interval["sum"])
            if s.get("omitted", False):
                continue
            a, b = number(s["start"], "start"), number(s["end"], "end")
            seconds = number(s["seconds"], "seconds")
            size = number(s["bytes"], "bytes")
            if not 0 < b-a <= 2.5 or abs(seconds-(b-a)) > 0.05:
                raise InvalidReceipt("use complete, approximately 1-second intervals")
            if samples and abs(a-samples[-1][1]) > 0.05:
                raise InvalidReceipt("gap or overlap in receiver intervals")
            samples.append((a, b, size))
            if protocol == "UDP":
                packets = number(s["packets"], "packets")
                lost = number(s["lost_packets"], "lost_packets")
                if packets <= 0 or lost > packets:
                    raise InvalidReceipt("invalid UDP packet accounting")
                losses.append(100*lost/packets)
                jitters.append(number(s["jitter_ms"], "jitter_ms"))
        if not samples or samples[0][0] > 1.0:
            raise InvalidReceipt("receiver intervals missing at beginning")
        span = samples[-1][1]-samples[0][0]
        if abs(summary_seconds-span) > max(1, span*0.01) or summary_bytes <= 0:
            raise InvalidReceipt("summary does not cover receiver intervals")
        received = sum(s[2] for s in samples)*8/span/1_000_000
        # Windows start at every sample boundary; partial final windows excluded.
        # Weight boundary intervals by overlap. Keep -i 1 for useful resolution.
        windows = []
        ends = [s[1] for s in samples]
        cumulative = [0]
        for _, _, size in samples:
            cumulative.append(cumulative[-1]+size)

        def bytes_at(t):
            index = bisect.bisect_right(ends, t)
            if index == len(samples):
                return cumulative[-1]
            a, b, size = samples[index]
            return cumulative[index]+size*max(0, min(1, (t-a)/(b-a)))

        for begin, _, _ in samples:
            finish = begin+window
            if finish > samples[-1][1]+0.001:
                break
            size = bytes_at(finish)-bytes_at(begin)
            windows.append(size*8/window/1_000_000)
        failures = []
        if span+0.05 < duration:
            failures.append("insufficient receiver duration")
        if received < target:
            failures.append("aggregate receiver bitrate below target")
        if not windows or min(windows) < target:
            failures.append("rolling receiver window below target or absent")
        if losses and max(losses) > max_loss:
            failures.append("UDP interval loss above limit")
        if jitters and max(jitters) > max_jitter:
            failures.append("UDP interval jitter above limit")
        return {"passed": not failures, "protocol": protocol, "scope": "receiver_application_goodput",
                "seconds": span, "received_mbps": received, "minimum_window_mbps": min(windows) if windows else None,
                "window_seconds": window, "maximum_udp_interval_loss_percent": max(losses) if losses else None,
                "maximum_udp_interval_jitter_ms": max(jitters) if jitters else None, "failures": failures}
    except (KeyError, TypeError, AttributeError) as exc:
        raise InvalidReceipt("missing or unsupported receiver JSON fields") from exc


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("receipt")
    parser.add_argument("--target-mbps", type=float, default=300)
    parser.add_argument("--duration", type=float, default=1800)
    parser.add_argument("--window", type=float, default=10)
    parser.add_argument("--max-loss-percent", type=float, default=1)
    parser.add_argument("--max-jitter-ms", type=float, default=20)
    args = parser.parse_args()
    try:
        with open(args.receipt, encoding="utf-8") as source:
            document = json.load(source, parse_constant=lambda _: (_ for _ in ()).throw(InvalidReceipt("nonfinite JSON")))
        result = analyze(document, target=args.target_mbps, duration=args.duration, window=args.window,
                         max_loss=args.max_loss_percent, max_jitter=args.max_jitter_ms)
        print(json.dumps(result, indent=2, allow_nan=False))
        return 0 if result["passed"] else 1
    except (InvalidReceipt, OSError, json.JSONDecodeError) as exc:
        print(f"invalid receipt: {exc}", file=sys.stderr)
        return 2


if __name__ == "__main__":
    sys.exit(main())
