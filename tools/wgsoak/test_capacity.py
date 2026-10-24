"""Capacity targets must reach both steady and post-outage receiver gates."""
import unittest
import run
from pathlib import Path
import tempfile
from unittest.mock import patch


def receiver(mbps):
    return dict(start=dict(test_start=dict(protocol='TCP')),
                intervals=[dict(sum=dict(start=i, end=i+1, seconds=1,
                                         bytes=mbps*1e6/8, sender=False)) for i in range(6)],
                end=dict(sum_received=dict(seconds=6, bytes=mbps*1e6/8*6, sender=False)))


class CapacityTests(unittest.TestCase):
    def test_requested_tcp_protocol_must_match_both_actual_peers(self):
        paths=[dict(kind='https-connect',cost_ms=0),dict(kind='quic-stream',cost_ms=20)]
        actual={role:dict(carriers=[dict(p) for p in paths]) for role in ('edge','exit')}
        run.check_redundant_setup(actual,False,preferred='https',tcp_mode='https-connect')
        actual['exit']['carriers'][0]['kind']='https'
        with self.assertRaisesRegex(ValueError,'TCP mode'):
            run.check_redundant_setup(actual,False,preferred='https',tcp_mode='https-connect')

    def test_large_packets_need_explicit_mode_and_keep_replay_byte_budget(self):
        run.check_packet_profile(60000,65507,True)
        ceiling=(256<<20)//65507
        run.check_replay_profile(ceiling,65507,False,True)
        for packet in (65508,65535):
            with self.assertRaises(ValueError):run.check_packet_profile(60000,packet,True)
        with self.assertRaises(ValueError):run.check_packet_profile(60000,65507)
        with self.assertRaises(ValueError):run.check_replay_profile(ceiling+1,65507,False,True)

    def test_replay_count_and_byte_budgets_match_runtime_bounds(self):
        self.assertTrue(hasattr(run,'check_replay_profile'),'missing bounded replay preflight')
        for count,packet,ordered in ((8192,2048,False),(131072,2048,False),(32768,8192,False),(16384,8192,True)):
            run.check_replay_profile(count,packet,ordered)
        for count,packet,ordered in ((127,2048,False),(131073,2048,False),(32769,8192,False),(16385,2048,True),(8192,0,False)):
            with self.subTest(count=count,packet=packet,ordered=ordered),self.assertRaises(ValueError):
                run.check_replay_profile(count,packet,ordered)

    def test_oversized_replay_is_rejected_before_lab_mutation(self):
        with tempfile.TemporaryDirectory() as directory:
            out=Path(directory)/'must-not-exist'
            args=['run.py','--binary','unused','--out',str(out),'--retained-packets','32769','--max-packet-bytes','8192']
            with patch('sys.argv',args),patch.object(run,'docker',side_effect=AssertionError('Docker called before budget validation')):
                with self.assertRaisesRegex(ValueError,'replay'):
                    run.main()
            self.assertFalse(out.exists())

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
