"""Validate the exact ICMP observer used for the monotonic latency control."""
import unittest
from icmp_monotonic import packet, checksum, reply_sequence


class MonotonicProtocolTests(unittest.TestCase):
    def test_checksum_and_matching_echo_reply(self):
        cookie = bytes(range(16))
        wire = packet(0, 41, 7, cookie)
        self.assertEqual(len(wire), 64)
        self.assertEqual(checksum(wire), 0)
        self.assertEqual(reply_sequence(wire, 41, cookie), 7)

    def test_other_identity_type_cookie_or_corruption_is_rejected(self):
        cookie = bytes(range(16))
        wire = packet(0, 41, 7, cookie)
        self.assertIsNone(reply_sequence(wire, 42, cookie))
        self.assertIsNone(reply_sequence(wire, 41, bytes(16)))
        self.assertIsNone(reply_sequence(packet(8, 41, 7, cookie), 41, cookie))
        self.assertIsNone(reply_sequence(wire[:-1] + b'Z', 41, cookie))


if __name__ == '__main__':
    unittest.main()
