import unittest
from memory import analyze_memory


class MemoryTests(unittest.TestCase):
    def samples(self, leak=False):
        return [dict(time=i, pid=12, start_ticks=55,
                     rss_kib=30000 + (i*60 if leak else (i % 30)*100),
                     pss_kib=25000 + (i*60 if leak else (i % 30)*100))
                for i in range(0, 1801, 5)]

    def test_bounded_gc_cycles_pass(self):
        self.assertTrue(analyze_memory(self.samples(), 0, 1800)['passed'])

    def test_continuing_resident_growth_fails(self):
        r = analyze_memory(self.samples(True), 0, 1800)
        self.assertFalse(r['passed'])
        self.assertGreater(r['pss_slope_mib_per_minute'], .5)

    def test_missing_samples_or_restarted_process_cannot_pass(self):
        for rows in [self.samples()[:-30], self.samples()[::10]]:
            with self.assertRaises(ValueError):
                analyze_memory(rows, 0, 1800)
        rows = self.samples()
        rows[-1]['start_ticks'] = 56
        with self.assertRaises(ValueError):
            analyze_memory(rows, 0, 1800)

    def test_continuing_rss_growth_is_not_hidden_by_flat_pss(self):
        rows = self.samples()
        for row in rows:
            row['rss_kib'] = 30000 + row['time']*.6*1024/60
        self.assertFalse(analyze_memory(rows, 0, 1800)['passed'])


if __name__ == '__main__':
    unittest.main()
