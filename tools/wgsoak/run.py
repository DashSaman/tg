"""Run kernel WireGuard -> unmodified TG -> kernel WireGuard in isolated containers.

Requires Docker Linux containers and the tg-wgsoak:local image. No host ports,
routes, interfaces or firewall rules are changed. Private volume is deleted on exit.
"""
import argparse
import hashlib
import json
import math
import os
from pathlib import Path
import subprocess
import sys
import time
import uuid

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
from acceptance import analyze
from memory import analyze_memory
from recovery import assess_recovery
from qos import verify_profile


def docker(*args, check=True):
    p = subprocess.run(['docker', *map(str, args)], capture_output=True, text=True, encoding='utf-8', errors='replace')
    if check and p.returncode:
        raise RuntimeError('docker '+str(args[0])+': '+p.stderr.strip()+p.stdout.strip())
    return p


def fault_schedule(kind,seconds):
    plan=[]
    for base in (0,60,120):
        if kind=='single-path':
            plan += [(base+10,'udp',True),(base+20,'udp',False),(base+35,'tcp',True),(base+45,'tcp',False)]
        else:
            plan += [(base+10,'udp',True),(base+16,'udp',False),(base+25,'tcp',True),(base+31,'tcp',False),
                     (base+40,'udp',True),(base+40,'tcp',True),(base+43,'udp',False),(base+43,'tcp',False)]
    result=[];active=set();last_clear=0
    for event in plan:
        at,protocol,blocked=event
        if at>seconds-10:break
        result.append(event)
        if blocked:active.add(protocol)
        else:active.remove(protocol)
        if not active:last_clear=len(result)
    return result[:last_clear]


def check_redundant_setup(configuration,required,udp_mode='auto',ordered=False,pacing=0,preferred='auto',replica_delay=None,udp_gro=False,max_packet=None,retained_packets=None,stream_window=None,stream_packets=False,tls_record_batch=False):
    for role in ('edge','exit'):
        if bool(configuration[role].get('tls_record_batch',False))!=tls_record_batch:
            raise ValueError('requested TLS record batching differs from actual '+role+' configuration')
        if bool(configuration[role].get('stream_packets',False))!=stream_packets:
            raise ValueError('requested complete packet mode differs from actual '+role+' configuration')
        if bool(configuration[role].get('redundant',False))!=required:
            raise ValueError('requested redundancy differs from actual '+role+' configuration')
        if bool(configuration[role].get('ordered_output',False))!=ordered:
            raise ValueError('requested output ordering differs from actual '+role+' configuration')
        if configuration[role].get('app_pacing_mbps',0)!=pacing:
            raise ValueError('requested application pacing differs from actual '+role+' configuration')
        if replica_delay is not None and configuration[role].get('replica_delay_ms')!=replica_delay:
            raise ValueError('requested backup delay differs from actual '+role+' configuration')
        if bool(configuration[role].get('app_udp_gro',False))!=udp_gro:
            raise ValueError('requested application UDP GRO differs from actual '+role+' configuration')
        if max_packet is not None and configuration[role].get('max_packet_bytes')!=max_packet:
            raise ValueError('requested maximum packet budget differs from actual '+role+' configuration')
        if retained_packets is not None and configuration[role].get('retained_packets',8192)!=retained_packets:
            raise ValueError('requested replay packet budget differs from actual '+role+' configuration')
        if stream_window is not None and configuration[role].get('stream_window_bytes',0)!=stream_window:
            raise ValueError('requested stream window differs from actual '+role+' configuration')
        if preferred!='auto':
            paths=configuration[role]['carriers']
            favored=lambda p:p['kind']=='https' if preferred=='https' else p['kind'] in ('quic','quic-stream')
            if not any(favored(p) for p in paths) or not any(not favored(p) for p in paths) or any(p.get('cost_ms')!=(0 if favored(p) else 20) for p in paths):
                raise ValueError('requested preferred carrier differs from actual '+role+' configuration')
        if udp_mode!='auto':
            native=[p['kind'] for p in configuration[role]['carriers'] if p['kind'] in ('quic','quic-stream')]
            if not native or any(kind!=udp_mode for kind in native):
                raise ValueError('requested UDP mode differs from actual '+role+' configuration')

def check_qos_setup(reports,required):
    if not required:return
    for role in ('edge','exit'):
        report=reports[role]
        if report.get('interface')!='wg-tg' or report.get('requested_mbps')!=required:
            raise ValueError('requested WireGuard QoS differs from actual '+role+' setup')
        verify_profile(report['qdiscs'],report['classes_text'],required)

def check_qos_activity(reports,required):
    if not required:return True
    return all(any(q.get('handle')==handle and q.get('packets',0)>0 for q in reports[role]['qdiscs'])
               for role in ('edge','exit') for handle in ('7a00:','7a10:'))


def analyze_trial(document, seconds, window, target, health=None):
    result = analyze(document, duration=seconds, window=window, target=target)
    result['target_mbps'] = target
    if health is not None:
        direct = document.get('intervals', [])
        receiver = document if direct and direct[0].get('sum', {}).get('sender') is False else document['server_output_json']
        result.update(assess_recovery(receiver['intervals'], result, seconds, health, target=target))
    return result


def check_packet_profile(mtu,max_packet,stream_packets=False):
    limit=65507 if stream_packets else 8192
    if not 512<=max_packet<=limit or not 576<=mtu or ((mtu+15)//16)*16+32>max_packet:
        raise ValueError('inner MTU plus WireGuard padding/overhead must fit the selected TG packet budget')


def check_replay_profile(packets,max_packet,ordered=False,stream_packets=False):
    limit=65507 if stream_packets else 8192
    if (not 512<=max_packet<=limit or not 128<=packets<=131072
            or packets*max_packet>(256<<20) or (ordered and packets>16384)):
        raise ValueError('replay budget must be128..131072 packets, at most256MiB payload and16384 packets for ordered output')


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--binary', type=Path, required=True)
    parser.add_argument('--out', type=Path, required=True)
    parser.add_argument('--seconds', type=int, default=1800)
    parser.add_argument('--fault-seconds', type=int, default=180)
    parser.add_argument('--probe-only', choices=['https', 'quic', 'both', 'direct'])
    parser.add_argument('--block-size', default='128K')
    parser.add_argument('--queue-frames', type=int, default=1024)
    parser.add_argument('--skip-https-probe', action='store_true')
    parser.add_argument('--profile', action='store_true', help='set lab-only profiling wrapper environment')
    parser.add_argument('--gomaxprocs', type=int, default=0, help='diagnostic scheduler control; 0 keeps runtime default')
    parser.add_argument('--carrier', choices=['both', 'quic', 'quic-stream', 'https'], default='both')
    parser.add_argument('--udp-mode',choices=['auto','quic','quic-stream'],default='auto')
    parser.add_argument('--mtu', type=int, default=1280)
    parser.add_argument('--max-packet-bytes',type=int,default=2048,help='TG original packet budget; must include inner MTU plus WireGuard overhead')
    parser.add_argument('--retained-packets',type=int,default=8192,help='bounded sender log;128..131072 packets within256MiB payload budget')
    parser.add_argument('--stream-window-bytes',type=int,default=0,help='0 keeps native HTTP2/QUIC windows; explicit1..256MiB per stream')
    parser.add_argument('--stream-packets',action='store_true',help='opt-in complete-packet reliable protocol; both peers must support it')
    parser.add_argument('--tls-record-batch',action='store_true',help='experimental encrypted TLS write coalescing; disabled by default')
    parser.add_argument('--mbps', type=float, default=350, help='aggregate TCP pacing target; 0 uses unpaced bulk TCP')
    parser.add_argument('--target-mbps', type=float, default=300, help='required receiver floor for steady traffic and recovery; distinct from offered --mbps')
    parser.add_argument('--redundant',action='store_true')
    parser.add_argument('--ordered-output',action='store_true',help='sequence barrier control; default preserves independent UDP delivery')
    parser.add_argument('--app-pacing-mbps',type=int,default=0,help='optional TG local UDP output cap; 0 disables; distinct from TCP offered rate')
    parser.add_argument('--app-udp-gro',action='store_true',help='opt-in Linux application UDP GRO; verify actual counters before attributing performance')
    parser.add_argument('--replica-delay-ms',type=int,default=10,help='bounded delay for unacknowledged backup copies, 0..50ms')
    parser.add_argument('--wg-qos-mbps',type=int,default=0,help='optional private WG egress HTB/FQ-CoDel profile; 0 disables; never changes host interfaces')
    parser.add_argument('--preferred-carrier',choices=['auto','udp','https'],default='auto',help='diagnostic deferred-copy preference; both carrier kinds remain enabled')
    parser.add_argument('--window',type=float,default=10,help='receiver rolling window in seconds; 1 is stricter than original acceptance')
    parser.add_argument('--fault-plan',choices=['mixed','single-path'],default='mixed')
    parser.add_argument('--extra-mixed-faults',action='store_true',help='also repeat mixed/all-path cuts after the main fault phase')
    args = parser.parse_args()
    if args.seconds < 30 or args.fault_seconds < 90:
        raise ValueError('use >=30 steady seconds and >=90 fault seconds; only >=1800 qualifies as a soak')
    if not math.isfinite(args.target_mbps) or not 0 < args.target_mbps <= 100000:
        raise ValueError('receiver target must be finite and in 0..100000 Mbps, excluding zero')
    if not 0<=args.replica_delay_ms<=50:raise ValueError('backup delay must be 0..50ms')
    check_packet_profile(args.mtu,args.max_packet_bytes,args.stream_packets)
    if args.stream_packets and (not args.redundant or args.udp_mode=='quic' or args.carrier=='quic'):
        raise ValueError('complete packet mode requires acknowledged reliable carriers')
    check_replay_profile(args.retained_packets,args.max_packet_bytes,args.ordered_output,args.stream_packets)
    if args.stream_window_bytes!=0 and not 1<<20<=args.stream_window_bytes<=256<<20:raise ValueError('stream window must be0 or1..256MiB')
    if args.ordered_output and not args.redundant:raise ValueError('ordered output requires redundancy')
    if not 0<=args.wg_qos_mbps<=100000:raise ValueError('WG QoS must be 0 or 1..100000 Mbps')
    if args.preferred_carrier!='auto' and (args.carrier!='both' or not args.redundant):raise ValueError('explicit carrier preference requires both redundant carriers')
    out = args.out.resolve()
    out.mkdir(parents=True, exist_ok=False)
    binary = args.binary.resolve()
    prefix = 'tg-soak-'+uuid.uuid4().hex[:8]
    network, volume = prefix+'-net', prefix+'-private'
    names = {role: prefix+'-'+role for role in ('edge', 'exit')}
    tools = Path(__file__).resolve().parent
    mounts = ['--mount', f'type=bind,source={tools},target=/tools,readonly',
              '--mount', f'type=bind,source={tools.parent / "qos.py"},target=/qos.py,readonly',
              '--mount', f'type=bind,source={binary},target=/tg,readonly',
              '--mount', f'type=bind,source={out},target=/out',
              '--mount', f'type=volume,source={volume},target=/private']
    manifest = dict(binary_sha256=hashlib.sha256(binary.read_bytes()).hexdigest(),
                    source_commit=docker('run', '--rm', '--mount', f'type=bind,source={binary},target=/tg,readonly', 'tg-wgsoak:local', 'go', 'version', '-m', '/tg').stdout,
                    seconds=args.seconds, fault_seconds=args.fault_seconds, streams=8,
                    gomaxprocs=args.gomaxprocs or None, live_receiver_telemetry=True,
                    offered_aggregate_mbps=args.mbps if args.mbps else None, tcp_pacing=bool(args.mbps), mtu=args.mtu, carrier=args.carrier, started_utc=time.strftime('%Y-%m-%dT%H:%M:%SZ', time.gmtime()),
                    target_mbps=args.target_mbps,
                    queue_frames=args.queue_frames, block_size=args.block_size,
                    redundant=args.redundant, window_seconds=args.window, fault_plan=args.fault_plan, extra_mixed_faults=args.extra_mixed_faults,
                    requested_udp_mode=args.udp_mode,
                    ordered_output=args.ordered_output,
                    application_pacing_mbps=args.app_pacing_mbps,
                    application_udp_gro=args.app_udp_gro, replica_delay_ms=args.replica_delay_ms,
                    max_packet_bytes=args.max_packet_bytes,
                    retained_packets=args.retained_packets,stream_window_bytes=args.stream_window_bytes,
                    stream_packets=args.stream_packets,
                    tls_record_batch=args.tls_record_batch,
                    wg_qos_mbps=args.wg_qos_mbps,
                    preferred_carrier=args.preferred_carrier,
                    harness_sources={path.relative_to(tools.parent).as_posix():hashlib.sha256(path.read_bytes()).hexdigest() for path in (tools/'run.py',tools/'node.py',tools/'recovery.py',tools/'memory.py',tools.parent/'qos.py',tools.parent/'acceptance.py')},
                    scope='two isolated network namespaces on one Linux Docker VM; kernel WireGuard and TG; no WAN or DPI')
    (out/'manifest.json').write_text(json.dumps(manifest, indent=2))
    created = []
    events = []

    def execute(role, *command):
        return docker('exec', names[role], *command).stdout

    def rule(protocol, blocked):
        execute('edge', 'iptables', '-w', '2', '-A' if blocked else '-D', 'OUTPUT', '-p', protocol,
                '--dport', '8444' if protocol == 'udp' else '8443', '-j', 'DROP')

    def rows(role):
        path = out/(role+'-monitor.jsonl')
        if not path.exists():
            return []
        lines = path.read_text().splitlines()
        return [json.loads(line) for line in lines if line.endswith('}')]

    def trial(case, seconds, faults=False, reverse=False, fault_kind=None):
        server_file = open(out/(case+'-server.json'), 'w')
        server = subprocess.Popen(['docker', 'exec', names['exit'], 'iperf3', '-s', '-1', '-B', '10.77.0.2', '-J'], stdout=server_file, stderr=subprocess.STDOUT)
        client_file = open(out/(case+'-client.json'), 'w')
        time.sleep(1)
        command = ['docker', 'exec', names['edge'], 'iperf3', '-c', '10.77.0.2', '-B', '10.77.0.1',
                   '-P', '8', '-t', str(seconds), '-i', '1', '-J', '--get-server-output']
        if args.mbps:
            command += ['-b', str(int(args.mbps*1e6/8))]
        command += ['-l', args.block_size]
        if reverse:
            command.append('-R')
        before = time.time()
        client = subprocess.Popen(command, stdout=client_file, stderr=subprocess.STDOUT)
        print('START '+case+' '+str(seconds)+'s', flush=True)
        plan = []
        if faults:
            plan = fault_schedule(fault_kind or args.fault_plan, seconds)
        next_event, report_at = 0, 60
        try:
            while client.poll() is None:
                elapsed = time.time()-before
                if elapsed > seconds+60:
                    raise TimeoutError(case+' did not finish')
                while next_event < len(plan) and elapsed >= plan[next_event][0]:
                    at, protocol, blocked = plan[next_event]
                    rule(protocol, blocked)
                    events.append(dict(case=case, time=time.time(), seconds=time.time()-before, protocol=protocol, blocked=blocked))
                    (out/'events.json').write_text(json.dumps(events, indent=2))
                    print('FAULT '+case+' '+str(round(time.time()-before, 2))+'s '+protocol+' '+str(blocked), flush=True)
                    next_event += 1
                if elapsed >= report_at:
                    status = {}
                    for role in names:
                        samples = rows(role)
                        if samples:
                            s = samples[-1]
                            status[role] = dict(rss_mib=round(s['rss_kib']/1024, 2), pss_mib=round(s['pss_kib']/1024, 2),
                                                healthy=s.get('tg', {}).get('healthy_carriers'), active=s.get('tg', {}).get('active_carrier'))
                    progress = dict(case=case, elapsed_seconds=round(elapsed), status=status)
                    print(json.dumps(progress), flush=True)
                    (out/'progress.json').write_text(json.dumps(progress, indent=2))
                    report_at += 60
                time.sleep(.2)
            server.wait(timeout=20)
        finally:
            if client.poll() is None:
                client.terminate()
                client.wait(timeout=10)
            if server.poll() is None:
                server.terminate()
                server.wait(timeout=10)
            client_file.close()
            server_file.close()
        after = time.time()
        if client.returncode or server.returncode:
            raise RuntimeError(case+' iperf exit '+str((client.returncode, server.returncode)))
        document = json.loads((out/(case+'-client.json')).read_text())
        # Retain ordinary speed failures during all-path cuts; the same selected
        # receiver target also governs recovery instead of a hidden 300 Mbps gate.
        health = {role: rows(role)[-1].get('tg', {}).get('healthy_carriers') for role in names} if faults else None
        result = analyze_trial(document, seconds, args.window, args.target_mbps, health)
        result.update(start=before, end=after, case=case, reverse=reverse)
        (out/(case+'-acceptance.json')).write_text(json.dumps(result, indent=2))
        print('RESULT '+json.dumps(result), flush=True)
        if not faults and not result['passed'] and args.probe_only:
            raise RuntimeError(case+' failed receiver speed gate; receipts retained')
        # Complete memory collection even if the receiver/recovery target fails.
        # The final summary and exit status keep that failed verdict explicit.
        return result

    try:
        docker('network', 'create', '--internal', '--label', 'tg.wgsoak='+prefix, network)
        docker('volume', 'create', '--label', 'tg.wgsoak='+prefix, volume)
        docker('run', '--rm', *mounts, 'tg-wgsoak:local', 'python3', '/tools/node.py', 'init', names['exit'], args.queue_frames, args.carrier, '1' if args.redundant else '0',args.udp_mode,'1' if args.ordered_output else '0',args.app_pacing_mbps,args.preferred_carrier,args.replica_delay_ms,'1' if args.app_udp_gro else '0',args.max_packet_bytes,args.retained_packets,args.stream_window_bytes,'1' if args.stream_packets else '0','1' if args.tls_record_batch else '0')
        for role in ('exit', 'edge'):
            docker('run', '-d', '--name', names[role], '--network', network, '--cap-add', 'NET_ADMIN',
                   *(['-e', 'TG_DIAGNOSTIC_PROFILE=/out/'+role] if args.profile else []),
                   *(['-e', 'GOMAXPROCS='+str(args.gomaxprocs)] if args.gomaxprocs else []),
                   '--label', 'tg.wgsoak='+prefix, *mounts, 'tg-wgsoak:local', 'python3', '/tools/node.py', role, args.mtu,args.wg_qos_mbps)
            created.append(names[role])
        for attempt in range(30):
            ping = docker('exec', names['edge'], 'ping', '-c', '1', '-W', '1', '10.77.0.2', check=False)
            if ping.returncode == 0:
                break
            time.sleep(1)
        else:
            raise RuntimeError('WireGuard handshake/connectivity failed; inspect node logs')
        manifest['versions'] = execute('edge', 'sh', '-c', 'uname -r; wg --version; iperf3 --version; ip -details link show wg-tg; ip route get 10.77.0.2')
        manifest['clock_ticks_per_second']=int(execute('edge','getconf','CLK_TCK'))
        manifest['kernel_wg_mtu']={role:json.loads(execute(role,'ip','-j','link','show','dev','wg-tg'))[0]['mtu'] for role in names}
        if any(mtu!=args.mtu for mtu in manifest['kernel_wg_mtu'].values()):
            raise ValueError('requested inner MTU differs from actual kernel WG interface')
        manifest['lab_configuration'] = json.loads((out/'lab-config-summary.json').read_text())
        check_redundant_setup(manifest['lab_configuration'],args.redundant,args.udp_mode,args.ordered_output,args.app_pacing_mbps,args.preferred_carrier,args.replica_delay_ms,args.app_udp_gro,args.max_packet_bytes,args.retained_packets,args.stream_window_bytes,args.stream_packets,args.tls_record_batch)
        if args.wg_qos_mbps:
            manifest['qos_configuration']={role:json.loads(execute(role,'python3','/qos.py','check','--interface','wg-tg','--mbps',args.wg_qos_mbps)) for role in names}
            check_qos_setup(manifest['qos_configuration'],args.wg_qos_mbps)
        (out/'manifest.json').write_text(json.dumps(manifest, indent=2))
        for role in names:
            (out/(role+'-udp-socket-memory.txt')).write_text(execute(role,'ss','-u','-a','-n','-m'))
        docker('exec','-d',names['exit'],'python3','/tools/live.py')
        if args.probe_only:
            if args.probe_only == 'https':
                rule('udp', True)
                time.sleep(6)
            if args.probe_only == 'direct':
                execute('edge', 'python3', '/tools/node.py', 'direct', names['exit'])
            trial(args.probe_only+'-probe', args.seconds)
            return 0
        # A short full-stack HTTPS probe precedes the long normal-path run.
        if not args.skip_https_probe:
            rule('udp', True)
            time.sleep(6)
            trial('https-probe', 30)
            rule('udp', False)
            time.sleep(12)
        steady = trial('steady', args.seconds)
        # Do not restart TG: the same processes continue through all fault cycles.
        fault = trial('faults', args.fault_seconds, faults=True)
        additional=trial('all-path-faults',args.fault_seconds,faults=True,fault_kind='mixed') if args.extra_mixed_faults else None
        time.sleep(6)
        result = dict(steady=steady, faults=fault, memory={}, scope=manifest['scope'])
        if additional is not None:result['additional_faults']=additional
        result['single_path_speed_gate_required']=args.fault_plan=='single-path'
        if args.wg_qos_mbps:
            result['qos_final']={role:json.loads(execute(role,'python3','/qos.py','check','--interface','wg-tg','--mbps',args.wg_qos_mbps)) for role in names}
        result['qos_activity_passed']=check_qos_activity(result.get('qos_final',{}),args.wg_qos_mbps)
        if args.seconds >= 1800:
            for role in names:
                result['memory'][role] = analyze_memory(rows(role), steady['start'], steady['start']+args.seconds)
        else:
            result['memory_note'] = 'short setup validation only; not 30-minute acceptance'
        result['passed'] = steady['passed'] and fault['recovery_passed'] and (fault['passed'] if args.fault_plan=='single-path' else True) and (additional['recovery_passed'] if additional else True) and all(m['passed'] for m in result['memory'].values())
        result['passed']=result['passed'] and result['qos_activity_passed']
        (out/'summary.json').write_text(json.dumps(result, indent=2))
        print('SUMMARY '+json.dumps(result), flush=True)
        return 0 if result['passed'] else 1
    finally:
        for role, name in names.items():
            if name in created:
                (out/(role+'-node.log')).write_text(docker('logs', name, check=False).stdout + docker('logs', name, check=False).stderr)
                docker('rm', '-f', name, check=False)
        docker('network', 'rm', network, check=False)
        docker('volume', 'rm', volume, check=False)
        (out/'cleanup.json').write_text(json.dumps(dict(containers=created, network=network, private_volume=volume,
            remaining_containers=docker('ps', '-aq', '--filter', 'label=tg.wgsoak='+prefix).stdout.strip(),
            remaining_network=docker('network', 'ls', '-q', '--filter', 'label=tg.wgsoak='+prefix).stdout.strip(),
            remaining_private_volume=docker('volume', 'ls', '-q', '--filter', 'label=tg.wgsoak='+prefix).stdout.strip()), indent=2))


if __name__ == '__main__':
    raise SystemExit(main())
