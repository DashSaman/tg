"""Disposable Linux container setup and external daemon monitoring. Never emit keys."""
import json
import os
from pathlib import Path
import signal
import subprocess
import sys
import time
import urllib.request


def command(*args, **kwargs):
    return subprocess.run(args, check=True, capture_output=True, text=True, **kwargs).stdout.strip()


def initialize(exit_name, queue, carrier, redundant='0', udp_mode='auto', ordered='0', pacing='0', preferred='auto',replica_delay='10',udp_gro='0'):
    os.umask(0o077)
    command('/tg', 'init', '--dir', '/private/pki', '--server-name', 'localhost', *(['--redundant'] if redundant=='1' else []))
    public = {}
    for role in ('edge', 'exit'):
        p = Path('/private/pki') / (role+'.json')
        config = json.loads(p.read_text())
        config['metrics'] = '127.0.0.1:19090'
        config['queue_frames'] = int(queue)
        config['ordered_output'] = ordered=='1'
        config['app_pacing_mbps'] = int(pacing)
        config['replica_delay_ms'] = int(replica_delay)
        if udp_gro=='1':config['app_udp_gro']=True
        if udp_mode!='auto':
            for path in config['carriers']:
                if path['kind'] in ('quic','quic-stream'):path['kind']=udp_mode
        if carrier in ('quic','quic-stream'):
            config['carriers'] = [c for c in config['carriers'] if c['kind'] in ('quic','quic-stream')]
            for c in config['carriers']:
                c['kind'] = carrier
        elif carrier != 'both':
            config['carriers'] = [c for c in config['carriers'] if c['kind'] == carrier]
        for path in config['carriers']:
            if preferred!='auto':
                favored=path['kind']=='https' if preferred=='https' else path['kind'] in ('quic','quic-stream')
                path['cost_ms']=0 if favored else 20
            port = 8443 if path['kind'] == 'https' else 8444
            path['address'] = f'{exit_name if role == "edge" else "0.0.0.0"}:{port}'
        p.write_text(json.dumps(config))
        public[role] = {k:config.get(k) for k in ('auto_upgrade','redundant','ordered_output','replica_delay_ms','app_pacing_mbps','app_udp_gro','queue_frames','max_packet_bytes','probe_interval_ms','probe_timeout_ms','hold_down_ms')}
        public[role]['carriers'] = [dict(name=c['name'],kind=c['kind'],cost_ms=c.get('cost_ms',0)) for c in config['carriers']]
        key = command('wg', 'genkey')
        Path('/private/'+role+'.key').write_text(key+'\n')
        Path('/private/'+role+'.pub').write_text(command('wg', 'pubkey', input=key)+'\n')
    Path('/out/lab-config-summary.json').write_text(json.dumps(public,indent=2))
    print('temporary credentials generated; no keys exported', flush=True)


def run(role, mtu, qos='0'):
    other = 'exit' if role == 'edge' else 'edge'
    address, peer = ('10.77.0.1', '10.77.0.2') if role == 'edge' else ('10.77.0.2', '10.77.0.1')
    command('ip', 'link', 'add', 'dev', 'wg-tg', 'type', 'wireguard')
    command('ip', 'address', 'add', address+'/30', 'dev', 'wg-tg')
    args = ['wg', 'set', 'wg-tg', 'listen-port', '51820', 'private-key', '/private/'+role+'.key',
            'peer', Path('/private/'+other+'.pub').read_text().strip(), 'allowed-ips', peer+'/32']
    if role == 'edge':
        args += ['endpoint', '127.0.0.1:31820', 'persistent-keepalive', '5']
    command(*args)
    command('ip', 'link', 'set', 'dev', 'wg-tg', 'mtu', mtu)
    if int(qos)>0:
        profile=command('python3','/qos.py','apply','--interface','wg-tg','--mbps',qos)
        Path('/out/'+role+'-qos-start.json').write_text(profile)
    command('ip', 'link', 'set', 'dev', 'wg-tg', 'up')
    log = open('/out/'+role+'-tg.log', 'w', buffering=1)
    process = subprocess.Popen(['/tg', 'run', '--config', '/private/pki/'+role+'.json'], stdout=log, stderr=log)
    stopped = False

    def stop(*_):
        nonlocal stopped
        stopped = True
    signal.signal(signal.SIGTERM, stop)
    signal.signal(signal.SIGINT, stop)
    output = open('/out/'+role+'-monitor.jsonl', 'w', buffering=1)
    sample_number = 0
    try:
        while not stopped:
            if process.poll() is not None:
                raise RuntimeError('TG exited: '+str(process.returncode))
            status = {}
            for line in Path(f'/proc/{process.pid}/smaps_rollup').read_text().splitlines():
                if ':' in line:
                    key, value = line.split(':', 1)
                    if key in ('Rss', 'Pss'):
                        status[key.lower()+'_kib'] = int(value.split()[0])
            fields = Path(f'/proc/{process.pid}/stat').read_text().rsplit(')', 1)[1].split()
            row = dict(time=time.time(), pid=process.pid, start_ticks=int(fields[19]),
                       cpu_ticks=int(fields[11])+int(fields[12]), threads=int(fields[17]), **status)
            snmp = Path('/proc/net/snmp').read_text().splitlines()
            row['udp'] = next((dict(zip(a.split()[1:], map(int, b.split()[1:])))
                               for a, b in zip(snmp[::2], snmp[1::2]) if a.startswith('Udp:')), {})
            row['tcp'] = next((dict(zip(a.split()[1:], map(int, b.split()[1:])))
                               for a, b in zip(snmp[::2], snmp[1::2]) if a.startswith('Tcp:')), {})
            # Namespace-local socket receive queues and drops. Never collect keys.
            row['udp_sockets'] = [dict(family=family, local=p[1], remote=p[2], queue=p[4], inode=p[9], drops=int(p[-1]))
                                  for family in ('udp', 'udp6')
                                  for line in Path('/proc/net/'+family).read_text().splitlines()[1:]
                                  if len(p := line.split()) >= 13]
            try:
                with urllib.request.urlopen('http://127.0.0.1:19090/status', timeout=2) as response:
                    row['tg'] = json.load(response)
            except Exception as exc:
                row['status_error'] = type(exc).__name__
            row['wg_rx_bytes'] = int(Path('/sys/class/net/wg-tg/statistics/rx_bytes').read_text())
            row['wg_tx_bytes'] = int(Path('/sys/class/net/wg-tg/statistics/tx_bytes').read_text())
            row['wg_device_errors'] = {k:int(Path('/sys/class/net/wg-tg/statistics/'+k).read_text())
                                       for k in ('rx_dropped','tx_dropped','rx_errors','tx_errors')}
            row['cpu_pressure'] = Path('/proc/pressure/cpu').read_text().strip()
            row['cgroup_cpu_stat'] = Path('/sys/fs/cgroup/cpu.stat').read_text().strip()
            if int(qos)>0:
                row['wg_qdisc']=json.loads(command('tc','-s','-j','qdisc','show','dev','wg-tg'))
            if sample_number % 12 == 0:
                row['wg_latest_handshake'] = int(command('wg', 'show', 'wg-tg', 'latest-handshakes').split()[-1])
                transfer = command('wg', 'show', 'wg-tg', 'transfer').split()
                row['wg_peer_received_bytes'], row['wg_peer_sent_bytes'] = map(int, transfer[-2:])
            sample_number += 1
            current = Path('/sys/fs/cgroup/memory.current')
            if current.exists():
                row['container_memory_bytes'] = int(current.read_text())
                row['container_memory_stat'] = {k: int(v) for k, v in
                    (line.split() for line in Path('/sys/fs/cgroup/memory.stat').read_text().splitlines())
                    if k in ('anon', 'file', 'kernel', 'sock', 'slab')}
            output.write(json.dumps(row)+'\n')
            for _ in range(50):
                if stopped:
                    break
                time.sleep(.1)
    finally:
        process.terminate()
        process.wait(timeout=10)
        output.close()
        log.close()


if __name__ == '__main__':
    if sys.argv[1] == 'init':
        initialize(*sys.argv[2:])
    elif sys.argv[1] == 'direct':
        command('wg', 'set', 'wg-tg', 'peer', Path('/private/exit.pub').read_text().strip(), 'endpoint', sys.argv[2]+':51820')
    else:
        run(*sys.argv[1:])
