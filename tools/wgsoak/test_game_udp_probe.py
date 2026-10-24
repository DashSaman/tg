import importlib.util
from pathlib import Path
import unittest

path=Path(__file__).with_name('game_udp_probe.py')
spec=importlib.util.spec_from_file_location('game_udp_probe',path)
probe=importlib.util.module_from_spec(spec);spec.loader.exec_module(probe)


class DatagramValidation(unittest.TestCase):
    def test_exact_cookie_sequence_and_timestamp_required(self):
        cookie=bytes(range(16));data=probe.packet(cookie,12,987654321)
        self.assertEqual(len(data),128)
        self.assertEqual(probe.decode(data,cookie),(12,987654321))
        self.assertIsNone(probe.decode(data,bytes(16)))
        self.assertIsNone(probe.decode(data[:-1],cookie))
        self.assertIsNone(probe.decode(data+b'0',cookie))
        damaged=bytearray(data);damaged[-1]=1
        self.assertIsNone(probe.decode(damaged,cookie))

    def test_wrong_sequence_or_timestamp_not_accepted_as_pending(self):
        cookie=bytes(range(16));pending={12:987654321}
        self.assertTrue(probe.matches(probe.packet(cookie,12,987654321),cookie,pending))
        self.assertFalse(probe.matches(probe.packet(cookie,13,987654321),cookie,pending))
        self.assertFalse(probe.matches(probe.packet(cookie,12,987654322),cookie,pending))

    def test_loss_and_late_delivery_fail_gaming_gate(self):
        self.assertTrue(probe.gaming_pass(100,100,[1,2,3],[1,1],0))
        self.assertFalse(probe.gaming_pass(100,99,[1,2,3],[1,1],0))
        self.assertFalse(probe.gaming_pass(100,100,[1,2,60],[1,58],0))
        self.assertFalse(probe.gaming_pass(100,100,[1,2,3],[1,1],1))


if __name__=='__main__':unittest.main()
