"""Capacity targets must reach both steady and post-outage receiver gates."""
import unittest
import run


def receiver(mbps):
    return dict(start=dict(test_start=dict(protocol='TCP')),
                intervals=[dict(sum=dict(start=i, end=i+1, seconds=1,
                                         bytes=mbps*1e6/8, sender=False)) for i in range(6)],
                end=dict(sum_received=dict(seconds=6, bytes=mbps*1e6/8*6, sender=False)))


class CapacityTests(unittest.TestCase):
    def test_inner_mtu_must_fit_ciphertext_packet_budget(self):
        self.assertTrue(hasattr(run, 'check_packet_profile'))
        run.check_packet_profile(1280,2048)
        run.check_packet_profile(7168,8192)
        for mtu,packet in ((7168,2048),(8161,8192),(1280,8193),(1280,511),(1,2048)):
            with self.subTest(mtu=mtu,packet=packet), self.assertRaises(ValueError):
                run.check_packet_profile(mtu,packet)

    def test_selected_capacity_applies_to_ordinary_and_recovery(self):
        self.assertTrue(hasattr(run, 'analyze_trial'), 'missing target-aware WireGuard trial analysis')
        ordinary = run.analyze_trial(receiver(350), 6, 1, 300)
        self.assertTrue(ordinary['passed'])
        high = run.analyze_trial(receiver(350), 6, 1, 10000, {'edge': 2, 'exit': 2})
        self.assertFalse(high['passed'])
        self.assertFalse(high['recovery_passed'])
        accepted = run.analyze_trial(receiver(10400), 6, 1, 10000, {'edge': 2, 'exit': 2})
        self.assertTrue(accepted['passed'])
        self.assertTrue(accepted['recovery_passed'])


if __name__ == '__main__':
    unittest.main()
