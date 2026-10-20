import unittest
from acceptance import analyze, InvalidReceipt


def receipt():
    return {"start": {"test_start": {"protocol": "TCP"}}, "intervals": [
        {"sum": {"start": i, "end": i+1, "seconds": 1, "bytes": 40_000_000, "sender": False, "omitted": False}}
        for i in range(4)], "end": {"sum_received": {"seconds": 4, "bytes": 160_000_000, "sender": False}}}


class AcceptanceTests(unittest.TestCase):
    def test_receiver_bytes_and_windows(self):
        r = receipt()
        result = analyze(r, duration=4, window=2)
        self.assertTrue(result["passed"])
        self.assertEqual(result["received_mbps"], 320)
        # A large claimed sender bitrate must not hide a receiver stall.
        r["intervals"][1]["sum"]["bytes"] = 0
        r["intervals"][1]["sum"]["bits_per_second"] = 10_000_000_000
        self.assertFalse(analyze(r, duration=4, window=2)["passed"])

    def test_incomplete_sender_and_nonfinite_rejected(self):
        variants = []
        r = receipt(); del r["end"]; variants.append(r)
        r = receipt(); r["intervals"][1]["sum"]["sender"] = True; variants.append(r)
        r = receipt(); r["intervals"][1]["sum"]["start"] = 0; variants.append(r)
        r = receipt(); r["intervals"][1]["sum"]["bytes"] = float("nan"); variants.append(r)
        r = receipt(); r["intervals"][1]["sum"]["bytes"] = -1; variants.append(r)
        r = receipt(); r["start"]["test_start"]["bidir"] = 1; variants.append(r)
        for r in variants:
            with self.subTest(receipt=r), self.assertRaises(InvalidReceipt):
                analyze(r, duration=4, window=2)
        self.assertFalse(analyze(receipt(), duration=1800)["passed"])

    def test_embedded_receiver_and_udp_metrics(self):
        r = receipt()
        self.assertTrue(analyze({"server_output_json": r}, duration=4, window=2)["passed"])
        r["start"]["test_start"]["protocol"] = "UDP"
        r["end"]["sum"] = r["end"].pop("sum_received")
        for interval in r["intervals"]:
            interval["sum"].update(packets=1000, lost_packets=1, jitter_ms=2)
        r["end"]["sum"].update(packets=4000, lost_packets=4, jitter_ms=2)
        self.assertTrue(analyze(r, duration=4, window=2)["passed"])
        r["intervals"][2]["sum"]["jitter_ms"] = 50
        self.assertFalse(analyze(r, duration=4, window=2)["passed"])
        del r["intervals"][2]["sum"]["lost_packets"]
        with self.assertRaises(InvalidReceipt):
            analyze(r, duration=4, window=2)

    def test_reverse_prefers_local_receiver_over_embedded_sender(self):
        r = receipt()
        sender = receipt()
        for interval in sender["intervals"]:
            interval["sum"]["sender"] = True
        r["server_output_json"] = sender
        self.assertTrue(analyze(r, duration=4, window=2)["passed"])


if __name__ == "__main__":
    unittest.main()
