"""Explicit optional WireGuard egress shaping; TG itself needs no network admin capability."""

import re
import json
import argparse
import subprocess

class ApplyError(RuntimeError):
    def __init__(self,cause,rollback):
        super().__init__(str(cause));self.cause=cause;self.rollback=rollback

def owned_root(qdiscs):
    roots=[q for q in qdiscs if q.get('root')]
    return len(roots)==1 and roots[0].get('kind')=='htb' and roots[0].get('handle')=='7a00:'

def verify_profile(qdiscs,classes_text,mbps):
    if type(mbps) is not int or not 1<=mbps<=100000 or not owned_root(qdiscs):
        raise ValueError('actual root qdisc or requested rate does not match')
    leaves=[q for q in qdiscs if q.get('kind')=='fq_codel' and q.get('handle')=='7a10:' and q.get('parent')=='7a00:10']
    if len(leaves)!=1:raise ValueError('actual FQ-CoDel leaf is absent or ambiguous')
    options=leaves[0].get('options',{})
    if options.get('limit')!=1024 or options.get('memory_limit')!=4194304 or options.get('ecn') is not True or not 4900<=options.get('target',0)<=5100 or not 99000<=options.get('interval',0)<=101000:
        raise ValueError('actual FQ-CoDel limits or delay settings differ')
    lines=re.findall(r'^class htb 7a00:10\b[^\n]*$',classes_text,re.M)
    if len(lines)!=1 or 'leaf 7a10:' not in lines[0]:raise ValueError('actual HTB class or leaf is absent')
    rates={}
    for key in ('rate','ceil'):
        match=re.search(r'\b'+key+r' (\d+(?:\.\d+)?)([KMG]?)bit\b',lines[0])
        if not match:raise ValueError('unrecognized actual class rate')
        rate=float(match[1])*{'':1,'K':1000,'M':1000000,'G':1000000000}[match[2]]/1000000
        # Older iproute2 prints class data as human-readable text even with -j.
        # Permit its display rounding, never an absent or substantially wrong rate.
        if abs(rate-mbps)>max(.000001,mbps*.001):raise ValueError('actual HTB rate/ceil differs from request')
        rates[key+'_mbps']=rate
    return dict(**rates,target_ms=options['target']/1000,interval_ms=options['interval']/1000,packet_limit=1024,memory_limit_bytes=4194304)

def check(interface,mbps,run):
    kind,_=inspect(interface,run)
    if kind!='wireguard':raise ValueError('only an explicit WireGuard interface is supported')
    qdiscs=json.loads(run(['tc','-s','-j','qdisc','show','dev',interface]))
    classes=run(['tc','-s','class','show','dev',interface])
    profile=verify_profile(qdiscs,classes,mbps)
    return dict(interface=interface,requested_mbps=mbps,profile=profile,qdiscs=qdiscs,classes_text=classes)

def inspect(interface,run):
    if not isinstance(interface,str) or re.fullmatch(r'[A-Za-z0-9_.-]{1,15}',interface) is None:
        raise ValueError('invalid interface name')
    links=json.loads(run(['ip','-d','-j','link','show','dev',interface]))
    qdiscs=json.loads(run(['tc','-j','qdisc','show','dev',interface]))
    return links[0].get('linkinfo',{}).get('info_kind'),qdiscs

def apply(interface,mbps,run):
    kind,qdiscs=inspect(interface,run)
    actions=plan(interface,mbps,kind,qdiscs)
    created=False
    try:
        for index,action in enumerate(actions):
            run(action)
            if index==0:created=True
        return dict(commands=actions,**check(interface,mbps,run))
    except (Exception,KeyboardInterrupt) as exc:
        rollback=dict(attempted=False,reason='root add did not complete')
        if created:
            try:
                kind,current=inspect(interface,run)
                if kind=='wireguard' and owned_root(current):
                    run(['tc','qdisc','del','dev',interface,'root','handle','7a00:'])
                    rollback=dict(attempted=True,succeeded=True)
                else:rollback=dict(attempted=False,reason='root changed; left untouched')
            except Exception as cleanup:
                rollback=dict(attempted=True,succeeded=False,error=str(cleanup),stderr=getattr(cleanup,'stderr',''))
        raise ApplyError(exc,rollback) from exc

def remove(interface,run):
    kind,qdiscs=inspect(interface,run)
    if kind!='wireguard' or not owned_root(qdiscs):
        raise ValueError('refusing to remove an unowned qdisc or non-WireGuard interface')
    run(['tc','qdisc','del','dev',interface,'root','handle','7a00:'])
    return dict(removed=True,qdiscs=json.loads(run(['tc','-j','qdisc','show','dev',interface])))

def plan(interface, mbps, link_kind, roots):
    if not isinstance(interface,str) or re.fullmatch(r'[A-Za-z0-9_.-]{1,15}',interface) is None:
        raise ValueError('invalid interface name')
    if type(mbps) is not int or not 1<=mbps<=100000:
        raise ValueError('rate must be an integer from 1 to 100000 Mbps')
    if link_kind!='wireguard':
        raise ValueError('only an explicit WireGuard interface is supported')
    root=[q for q in roots if q.get('root')]
    if len(root)>1 or any(q.get('kind') not in ('noqueue','noop') or q.get('handle')!='0:' for q in root):
        raise ValueError('existing custom root qdisc must not be replaced')
    return [
        ['tc','qdisc','add','dev',interface,'root','handle','7a00:','htb','default','10'],
        ['tc','class','add','dev',interface,'parent','7a00:','classid','7a00:10','htb','rate',f'{mbps}mbit','ceil',f'{mbps}mbit','burst','256k','cburst','256k','quantum','1514'],
        ['tc','qdisc','add','dev',interface,'parent','7a00:10','handle','7a10:','fq_codel','limit','1024','memory_limit','4mb','target','5ms','interval','100ms','ecn'],
    ]

def execute(command):
    return subprocess.run(command,check=True,capture_output=True,text=True).stdout

def main():
    parser=argparse.ArgumentParser(description='Explicit optional WireGuard egress shaping; never replace an existing custom root qdisc.')
    parser.add_argument('action',choices=('plan','apply','check','remove'))
    parser.add_argument('--interface',required=True)
    parser.add_argument('--mbps',type=int,default=400)
    args=parser.parse_args()
    try:
        if args.action=='apply':result=apply(args.interface,args.mbps,execute)
        elif args.action=='check':result=check(args.interface,args.mbps,execute)
        elif args.action=='remove':result=remove(args.interface,execute)
        else:
            kind,roots=inspect(args.interface,execute)
            result=dict(commands=plan(args.interface,args.mbps,kind,roots),mutates=False)
        print(json.dumps(result,indent=2));return 0
    except (Exception,KeyboardInterrupt) as exc:
        cause=exc.cause if isinstance(exc,ApplyError) else exc
        print(json.dumps(dict(error=str(cause),stderr=getattr(cause,'stderr',''),rollback=getattr(exc,'rollback',None)),indent=2));return 1

if __name__=='__main__':raise SystemExit(main())
