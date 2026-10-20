"""Recovery gate after receiver structure has been validated by acceptance.analyze."""


def assess_recovery(intervals, ordinary, expected_seconds, health):
    complete = [i for i in intervals if not i['sum'].get('omitted') and .8 <= i['sum']['seconds'] <= 1.2]
    final = complete[-5:]
    coverage = ordinary['seconds']+.05 >= expected_seconds
    recent = len(final) == 5 and final[-1]['sum']['end'] >= ordinary['seconds']-1.2
    speed = recent and all(i['sum'].get('sender') is False and i['sum']['bytes']*8/i['sum']['seconds']/1e6 >= 300 for i in final)
    return dict(recovered_last_5_intervals=speed, final_healthy=health,
                recovery_duration_covered=coverage,
                recovery_passed=coverage and speed and len(health) == 2 and all(n == 2 for n in health.values()))
