import unittest
import json
from qos import plan,apply,verify_profile

DEFAULT=[dict(kind='noqueue',handle='0:',root=True)]
INSTALLED=[dict(kind='htb',handle='7a00:',root=True),dict(kind='fq_codel',handle='7a10:',parent='7a00:10',options=dict(limit=1024,memory_limit=4194304,target=4999,interval=99999,ecn=True))]
CLASS='class htb 7a00:10 root leaf 7a10: prio 0 rate 400Mbit ceil 400Mbit burst 262050b cburst 262050b\n Sent 0 bytes 0 pkt'

class FailedInstall:
    def __init__(self,changed=False,kind='wireguard'):
        self.calls=[];self.installed=False;self.changed=changed;self.kind=kind
    def __call__(self,command):
        self.calls.append(command)
        if command[0]=='ip':return json.dumps([dict(linkinfo=dict(info_kind=self.kind))])
        if command[:3]==['tc','-j','qdisc']:
            root=dict(kind='fq_codel',handle='1234:',root=True) if self.changed and self.installed else dict(kind='htb',handle='7a00:',root=True)
            return json.dumps([root] if self.installed else DEFAULT)
        if command[:3]==['tc','qdisc','add'] and 'root' in command:self.installed=True
        if command[:3]==['tc','class','add']:raise RuntimeError('simulated class failure')
        if command[:3]==['tc','qdisc','del']:self.installed=False
        return '[]'

class Guards(unittest.TestCase):
    def test_actual_kernel_profile_and_rate_must_match(self):
        self.assertEqual(verify_profile(INSTALLED,CLASS,400)['rate_mbps'],400)
        for qdiscs,text in [(DEFAULT,CLASS),(INSTALLED,CLASS.replace('400Mbit','500Mbit')),(INSTALLED,CLASS.replace('7a00:10','7a00:99'))]:
            with self.assertRaises(ValueError):verify_profile(qdiscs,text,400)
    def test_wrong_delay_or_memory_settings_cannot_pass(self):
        for key,value in [('limit',10000),('memory_limit',33554432),('target',500000),('ecn',False)]:
            data=json.loads(json.dumps(INSTALLED));data[1]['options'][key]=value
            with self.assertRaises(ValueError):verify_profile(data,CLASS,400)
    def test_partial_apply_removes_only_its_new_owned_root(self):
        fake=FailedInstall()
        with self.assertRaises(RuntimeError):apply('wg0',400,fake)
        self.assertIn(['tc','qdisc','del','dev','wg0','root','handle','7a00:'],fake.calls)
        self.assertFalse(fake.installed)
    def test_apply_entry_rechecks_type_before_any_mutation(self):
        fake=FailedInstall(kind='ether')
        with self.assertRaises(ValueError):apply('eth0',400,fake)
        self.assertFalse(any(c[:3] in (['tc','qdisc','add'],['tc','class','add']) for c in fake.calls))
    def test_failed_apply_does_not_remove_a_replaced_root(self):
        fake=FailedInstall(changed=True)
        with self.assertRaises(RuntimeError):apply('wg0',400,fake)
        self.assertFalse(any(c[:3]==['tc','qdisc','del'] for c in fake.calls))
    def test_physical_interface_cannot_be_mutated(self):
        with self.assertRaises(ValueError):plan('eth0',400,'ether',DEFAULT)
    def test_existing_custom_root_is_never_replaced(self):
        for kind,handle in [('fq_codel','1234:'),('htb','7a00:')]:
            with self.subTest(kind=kind):
                with self.assertRaises(ValueError):plan('wg0',400,'wireguard',[dict(kind=kind,handle=handle,root=True)])
    def test_interface_and_rate_are_validated_before_commands(self):
        for name,rate in [('wg0;evil',400),('wg0',0),('wg0',100001),('wg0',True)]:
            with self.subTest(name=name,rate=rate):
                with self.assertRaises(ValueError):plan(name,rate,'wireguard',DEFAULT)
    def test_add_only_and_owned_handles_keep_changes_scoped(self):
        commands=plan('wg0',400,'wireguard',DEFAULT)
        self.assertEqual(len(commands),3)
        self.assertTrue(all(c[4:6]==['wg0','root'] or c[4:6]==['wg0','parent'] for c in commands))
        self.assertFalse(any('replace' in c or 'sysctl' in c for c in commands))

if __name__=='__main__':unittest.main()
