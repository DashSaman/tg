"""Run kernel WireGuard -> unmodified TG -> kernel WireGuard in isolated containers.

Requires Docker Linux containers and the tg-wgsoak:local image. No host ports,
routes, interfaces or firewall rules are changed. Private volume is deleted on exit.
"""
import argparse
import hashlib
import json
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


def docker(*args, check=True):
    p = subprocess.run(['docker', *map(str, args)], capture_output=True, text=True, encoding='utf-8', errors='replace')
    if check and p.returncode:
        raise RuntimeError('docker '+str(args[0])+': '+p.stderr.strip()+p.stdout.strip())
    return p


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
    parser.add_argument('--mtu', type=int, default=1280)
    parser.add_argument('--mbps', type=float, default=350, help='aggregate TCP pacing target; 0 uses unpaced bulk TCP')
    args = parser.parse_args()
    if args.seconds < 30 or args.fault_seconds < 90:
        raise ValueError('use >=30 steady seconds and >=90 fault seconds; only >=1800 qualifies as a soak')
    out = args.out.resolve()
    out.mkdir(parents=True, exist_ok=False)
    binary = args.binary.resolve()
    prefix = 'tg-soak-'+uuid.uuid4().hex[:8]
    network, volume = prefix+'-net', prefix+'-private'
    names = {role: prefix+'-'+role for role in ('edge', 'exit')}
    tools = Path(__file__).resolve().parent
    mounts = ['--mount', f'type=bind,source={tools},target=/tools,readonly',
              '--mount', f'type=bind,source={binary},target=/tg,readonly',
              '--mount', f'type=bind,source={out},target=/out',
              '--mount', f'type=volume,source={volume},target=/private']
    manifest = dict(binary_sha256=hashlib.sha256(binary.read_bytes()).hexdigest(),
                    source_commit=docker('run', '--rm', '--mount', f'type=bind,source={binary},target=/tg,readonly', 'tg-wgsoak:local', 'go', 'version', '-m', '/tg').stdout,
                    seconds=args.seconds, fault_seconds=args.fault_seconds, streams=8,
                    gomaxprocs=args.gomaxprocs or None, live_receiver_telemetry=True,
                    offered_aggregate_mbps=args.mbps if args.mbps else None, tcp_pacing=bool(args.mbps), mtu=args.mtu, carrier=args.carrier, started_utc=time.strftime('%Y-%m-%dT%H:%M:%SZ', time.gmtime()),
                    queue_frames=args.queue_frames, block_size=args.block_size,
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

    def trial(case, seconds, faults=False, reverse=False):
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
            for base in (0, 60, 120):
                plan += [(base+10, 'udp', True), (base+16, 'udp', False),
                         (base+25, 'tcp', True), (base+31, 'tcp', False),
                         (base+40, 'udp', True), (base+40, 'tcp', True),
                         (base+43, 'udp', False), (base+43, 'tcp', False)]
            plan = [p for p in plan if p[0] < seconds-10]
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
        if faults:
            # Retain ordinary acceptance verdict even when deliberate all-path cuts fail it.
            result = analyze(document, duration=seconds)
            receiver = document if reverse else document['server_output_json']
            health = {role: rows(role)[-1].get('tg', {}).get('healthy_carriers') for role in names}
            result.update(assess_recovery(receiver['intervals'], result, seconds, health))
        else:
            result = analyze(document, duration=seconds)
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
        docker('run', '--rm', *mounts, 'tg-wgsoak:local', 'python3', '/tools/node.py', 'init', names['exit'], args.queue_frames, args.carrier)
        for role in ('exit', 'edge'):
            docker('run', '-d', '--name', names[role], '--network', network, '--cap-add', 'NET_ADMIN',
                   *(['-e', 'TG_DIAGNOSTIC_PROFILE=/out/'+role] if args.profile else []),
                   *(['-e', 'GOMAXPROCS='+str(args.gomaxprocs)] if args.gomaxprocs else []),
                   '--label', 'tg.wgsoak='+prefix, *mounts, 'tg-wgsoak:local', 'python3', '/tools/node.py', role, args.mtu)
            created.append(names[role])
        for attempt in range(30):
            ping = docker('exec', names['edge'], 'ping', '-c', '1', '-W', '1', '10.77.0.2', check=False)
            if ping.returncode == 0:
                break
            time.sleep(1)
        else:
            raise RuntimeError('WireGuard handshake/connectivity failed; inspect node logs')
        manifest['versions'] = execute('edge', 'sh', '-c', 'uname -r; wg --version; iperf3 --version; ip -details link show wg-tg; ip route get 10.77.0.2')
        manifest['lab_configuration'] = json.loads((out/'lab-config-summary.json').read_text())
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
        time.sleep(6)
        result = dict(steady=steady, faults=fault, memory={}, scope=manifest['scope'])
        if args.seconds >= 1800:
            for role in names:
                result['memory'][role] = analyze_memory(rows(role), steady['start'], steady['start']+args.seconds)
        else:
            result['memory_note'] = 'short setup validation only; not 30-minute acceptance'
        result['passed'] = steady['passed'] and fault['recovery_passed'] and all(m['passed'] for m in result['memory'].values())
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
