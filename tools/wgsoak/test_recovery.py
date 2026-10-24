import unittest
from recovery import assess_recovery


class RecoveryTests(unittest.TestCase):
    def test_invalid_capacity_target_is_rejected(self):
        import inspect
        self.assertIn('target', inspect.signature(assess_recovery).parameters)
        for target in (0, -1, float('nan'), float('inf'), True):
            with self.subTest(target=target), self.assertRaises(ValueError):
                assess_recovery(self.intervals(6), {'seconds': 6}, 6,
                                {'edge': 2, 'exit': 2}, target=target)

    def test_ten_gigabit_recovery_cannot_pass_at_350_mbps(self):
        import inspect
        self.assertIn('target', inspect.signature(assess_recovery).parameters)
        rows = self.intervals(180)
        self.assertFalse(assess_recovery(rows, {'seconds': 180}, 180,
                                       {'edge': 2, 'exit': 2}, target=10000)['recovery_passed'])
        for row in rows:
            row['sum']['bytes'] = 1_300_000_000
        self.assertTrue(assess_recovery(rows, {'seconds': 180}, 180,
                                      {'edge': 2, 'exit': 2}, target=10000)['recovery_passed'])

    def intervals(self, seconds):
        return [dict(sum=dict(start=i, end=i+1, seconds=1, bytes=43750000, sender=False)) for i in range(seconds)]

    def test_truncated_receipt_cannot_pass_via_empty_final_slice(self):
        self.assertFalse(assess_recovery(self.intervals(1), {'seconds': 1}, 180, {'edge': 2, 'exit': 2})['recovery_passed'])

    def test_full_coverage_and_five_actual_final_intervals_pass(self):
        self.assertTrue(assess_recovery(self.intervals(180), {'seconds': 180}, 180, {'edge': 2, 'exit': 2})['recovery_passed'])

    def test_last_full_interval_is_not_silently_excluded(self):
        rows = self.intervals(180)
        rows[-1]['sum']['bytes'] = 0
        self.assertFalse(assess_recovery(rows, {'seconds': 180}, 180, {'edge': 2, 'exit': 2})['recovery_passed'])


if __name__ == '__main__':
    unittest.main()
