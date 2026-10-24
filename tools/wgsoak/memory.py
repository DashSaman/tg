"""Assess sampled daemon resident memory; this is not a proof of absence of leaks."""
import math
import statistics


def analyze_memory(samples, start, end):
    rows = [s for s in samples if start <= s['time'] <= end]
    if end-start < 1800 or not rows or rows[0]['time']-start > 10 or end-rows[-1]['time'] > 10:
        raise ValueError('incomplete 30-minute memory coverage')
    if any(b['time']-a['time'] > 10 or b['time'] <= a['time'] for a, b in zip(rows, rows[1:])):
        raise ValueError('missing/nonmonotonic memory samples')
    if len({(s['pid'], s['start_ticks']) for s in rows}) != 1:
        raise ValueError('daemon restarted during measurement')
    for s in rows:
        if any(not isinstance(s[k], (int, float)) or not math.isfinite(s[k]) or s[k] <= 0 for k in ('rss_kib', 'pss_kib')):
            raise ValueError('invalid resident-memory sample')
    early = [s for s in rows if start+300 <= s['time'] < start+600]
    late = [s for s in rows if end-300 <= s['time'] <= end]
    steady = [s for s in rows if s['time'] >= start+300]
    if len(early) < 50 or len(late) < 50:
        raise ValueError('insufficient steady-state memory samples')
    x = [(s['time']-start)/60 for s in steady]
    y = [s['pss_kib']/1024 for s in steady]
    mx, my = statistics.mean(x), statistics.mean(y)
    slope = sum((a-mx)*(b-my) for a, b in zip(x, y))/sum((a-mx)**2 for a in x)
    rss = [s['rss_kib']/1024 for s in steady]
    mrss = statistics.mean(rss)
    rss_slope = sum((a-mx)*(b-mrss) for a, b in zip(x, rss))/sum((a-mx)**2 for a in x)
    median = lambda rs, key: statistics.median(s[key] for s in rs)/1024
    rss_growth = median(late, 'rss_kib')-median(early, 'rss_kib')
    pss_growth = median(late, 'pss_kib')-median(early, 'pss_kib')
    failures = []
    if max(rss_growth, pss_growth) > 16:
        failures.append('resident median grew more than 16 MiB after warmup')
    if slope > .5:
        failures.append('PSS trend exceeds 0.5 MiB/min after first 5 minutes')
    if rss_slope > .5:
        failures.append('RSS trend exceeds 0.5 MiB/min after first 5 minutes')
    return dict(passed=not failures, failures=failures, sample_count=len(rows),
                rss_early_median_mib=median(early, 'rss_kib'), rss_late_median_mib=median(late, 'rss_kib'),
                pss_early_median_mib=median(early, 'pss_kib'), pss_late_median_mib=median(late, 'pss_kib'),
                rss_peak_mib=max(s['rss_kib'] for s in rows)/1024,
                pss_peak_mib=max(s['pss_kib'] for s in rows)/1024,
                rss_growth_mib=rss_growth, pss_growth_mib=pss_growth,
                pss_slope_mib_per_minute=slope,
                rss_slope_mib_per_minute=rss_slope,
                scope='Unmodified TG daemon /proc RSS/PSS, excluding iperf, monitor and kernel WireGuard memory; no leak-free guarantee beyond observed duration')
