import unittest
from run import fault_schedule,check_redundant_setup,check_qos_setup,check_qos_activity


class ScheduleTests(unittest.TestCase):
    def test_requested_qos_cannot_be_missing_on_either_peer(self):
        report=dict(interface='wg-tg',requested_mbps=0,qdiscs=[],classes_text='')
        with self.assertRaises(ValueError):check_qos_setup(dict(edge=report,exit=report),400)
    def test_installed_qdisc_must_actually_process_packets(self):
        reports={r:dict(qdiscs=[dict(handle=h,packets=10) for h in ('7a00:','7a10:')]) for r in ('edge','exit')}
        self.assertTrue(check_qos_activity(reports,400))
        reports['exit']['qdiscs'][1]['packets']=0
        self.assertFalse(check_qos_activity(reports,400))
        self.assertTrue(check_qos_activity({},0))
    def test_pacing_must_match_both_peer_configurations(self):
        c={r:dict(redundant=True,app_pacing_mbps=500) for r in ('edge','exit')}
        check_redundant_setup(c,True,pacing=500)
        c['exit']['app_pacing_mbps']=0
        with self.assertRaises(ValueError):check_redundant_setup(c,True,pacing=500)
    def test_ordering_control_cannot_silently_change(self):
        c={r:dict(redundant=True,ordered_output=True) for r in ('edge','exit')}
        with self.assertRaises(ValueError):check_redundant_setup(c,True)
        check_redundant_setup(c,True,ordered=True)
        c['exit']['ordered_output']=False
        with self.assertRaises(ValueError):check_redundant_setup(c,True,ordered=True)
    def test_udp_mode_override_is_verified_on_both_peers(self):
        c={r:dict(redundant=True,carriers=[dict(kind='quic-stream'),dict(kind='https')]) for r in ('edge','exit')}
        with self.assertRaises(ValueError):check_redundant_setup(c,True,'quic')
        c['edge']['carriers'][0]['kind']='quic'
        with self.assertRaises(ValueError):check_redundant_setup(c,True,'quic')
        c['exit']['carriers'][0]['kind']='quic'
        check_redundant_setup(c,True,'quic')
    def test_requested_mode_cannot_silently_disappear_at_container_boundary(self):
        c={role:{'redundant':False} for role in ('edge','exit')}
        with self.assertRaises(ValueError):check_redundant_setup(c,True)
        c['edge']['redundant']=True
        with self.assertRaises(ValueError):check_redundant_setup(c,True)
        c['exit']['redundant']=True
        check_redundant_setup(c,True)
    def test_every_supported_duration_ends_with_all_rules_restored(self):
        for kind in ('mixed','single-path'):
            for seconds in range(90,201):
                rules=set()
                for _,protocol,blocked in fault_schedule(kind,seconds):
                    if blocked:rules.add(protocol)
                    else:rules.remove(protocol)
                self.assertEqual(rules,set(),(kind,seconds))
    def test_single_path_never_blocks_both_and_restores_every_rule(self):
        rules=set()
        plan=fault_schedule('single-path',180)
        self.assertEqual(len(plan),12)
        for _,protocol,blocked in plan:
            if blocked:rules.add(protocol)
            else:rules.remove(protocol)
            self.assertLessEqual(len(rules),1)
        self.assertEqual(rules,set())

    def test_mixed_retains_three_explicit_all_path_cuts(self):
        rules=set();both=0
        for _,protocol,blocked in fault_schedule('mixed',180):
            if blocked:rules.add(protocol)
            else:rules.remove(protocol)
            if len(rules)==2:both+=1
        self.assertEqual(both,3)
        self.assertEqual(rules,set())
