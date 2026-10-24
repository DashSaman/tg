"""Bounded128-byte,50Hz UDP echo over private kernelWG; MONOTONIC RTT only.

Fixed private lab addresses/interface/port. Never binds a public endpoint. This
is an observer, not a TG service or production echo endpoint.
"""
import argparse
from array import array
import json
import math
import os
from pathlib import Path
import re
import select
import signal
import socket
import statistics
import struct
import time

MAGIC=b'TGGAME01'
PORT=19091


def packet(cookie,sequence,sent):
    assert len(cookie)==16
    return MAGIC+cookie+struct.pack('!QQ',sequence,sent)+bytes(88)


def decode(data,cookie):
    if len(data)!=128 or data[:8]!=MAGIC or data[8:24]!=cookie or data[40:]!=bytes(88):
        return None
    return struct.unpack('!QQ',data[24:40])


def matches(data,cookie,pending):
    fields=decode(data,cookie)
    return fields is not None and pending.get(fields[0])==fields[1]


def percentile(values,fraction=.95):
    return sorted(values)[math.ceil(len(values)*fraction)-1] if values else None


def gaming_pass(sent,received,rtts,variations,missed_deadlines):
    return bool(sent>=60 and received==sent and missed_deadlines==0 and rtts and variations and
                statistics.median(rtts)<20 and percentile(rtts)<30 and percentile(variations)<10)


def private_socket(address):
    sock=socket.socket(socket.AF_INET,socket.SOCK_DGRAM)
    sock.setsockopt(socket.SOL_SOCKET,socket.SO_BINDTODEVICE,b'wg-tg\0')
    sock.bind((address,PORT if address.endswith('.2') else 0))
    return sock


def server():
    sock=private_socket('10.77.0.2');sock.settimeout(.5)
    begin=time.monotonic();stopped=False;echoed=invalid=0
    def stop(*_):
        nonlocal stopped
        stopped=True
    signal.signal(signal.SIGTERM,stop);signal.signal(signal.SIGINT,stop)
    Path('/out/game-server-ready.json').write_text(json.dumps(dict(pid=os.getpid(),monotonic=time.monotonic())))
    try:
        while not stopped and time.monotonic()-begin<2400:
            try:data,peer=sock.recvfrom(129)
            except socket.timeout:continue
            if peer[0]!='10.77.0.1' or len(data)!=128 or data[:8]!=MAGIC or data[40:]!=bytes(88):
                invalid+=1;continue
            try:sock.sendto(data,peer);echoed+=1
            except OSError:invalid+=1
    finally:
        sock.close()
        Path('/out/game-server-result.json').write_text(json.dumps(dict(echoed=echoed,invalid=invalid,seconds=time.monotonic()-begin)))


def client(seconds,stem):
    info=time.get_clock_info('monotonic');assert info.monotonic and not info.adjustable
    sock=private_socket('10.77.0.1');sock.connect(('10.77.0.2',PORT));sock.setblocking(False)
    cookie=os.urandom(16);interval=20_000_000;timeout=1_000_000_000
    begin=time.monotonic_ns();finish=begin+int(seconds*1e9);next_send=begin
    pending={};sent=received=lost=ignored=send_errors=missed=peak=0
    rtts=array('d');variations=array('d');previous=None;last_received=None;max_gap_ns=0
    with Path('/out/'+stem+'-rows.jsonl').open('x',buffering=1) as rows:
        def emit(row):rows.write(json.dumps(row)+'\n')
        try:
            while time.monotonic_ns()<finish or pending:
                now=time.monotonic_ns()
                if now<finish and now>=next_send:
                    # No catch-up burst; missed20ms scheduling slots remain explicit.
                    missed+=max(0,(now-next_send)//interval)
                    seq=sent;sent+=1;stamp=time.monotonic_ns()
                    if len(pending)>=128:
                        raise RuntimeError('bounded pending probe budget exceeded')
                    try:
                        sock.send(packet(cookie,seq,stamp));pending[seq]=stamp
                        peak=max(peak,len(pending))
                    except OSError as exc:
                        send_errors+=1;emit(dict(sequence=seq,status='send_error',send_ns=stamp,error=type(exc).__name__))
                    next_send=stamp+interval
                now=time.monotonic_ns()
                for seq,stamp in list(pending.items()):
                    if now-stamp>=timeout:
                        lost+=1;pending.pop(seq);emit(dict(sequence=seq,status='timeout',send_ns=stamp))
                if now>=finish and not pending:break
                wait=.02
                if now<finish:wait=min(wait,max(0,(next_send-now)/1e9))
                if pending:wait=min(wait,max(0,(min(pending.values())+timeout-now)/1e9))
                if not select.select([sock],[],[],wait)[0]:continue
                for _ in range(256):
                    try:data=sock.recv(129)
                    except BlockingIOError:break
                    except OSError:ignored+=1;break
                    arrival=time.monotonic_ns()
                    if not matches(data,cookie,pending):ignored+=1;continue
                    seq,stamp=decode(data,cookie);pending.pop(seq)
                    if arrival-stamp>=timeout:
                        lost+=1;emit(dict(sequence=seq,status='late',send_ns=stamp,receive_ns=arrival));continue
                    rtt=(arrival-stamp)/1e6;received+=1;rtts.append(rtt)
                    if previous is not None and seq==previous[0]+1:variations.append(abs(rtt-previous[1]))
                    previous=(seq,rtt)
                    if last_received is not None:max_gap_ns=max(max_gap_ns,arrival-last_received)
                    last_received=arrival
                    emit(dict(sequence=seq,status='received',send_ns=stamp,receive_ns=arrival,rtt_ms=rtt))
        finally:sock.close()
    end=time.monotonic_ns()
    assert sent==received+lost+send_errors
    result=dict(clock='CLOCK_MONOTONIC via time.monotonic_ns',scope='Private kernelWG/TG128-byte UDP echo50Hz; user-space RTT includes scheduling, not one-way/RTP jitter.',
                requested_seconds=seconds,start_monotonic_ns=begin,end_monotonic_ns=end,
                sent=sent,received=received,lost=lost,send_errors=send_errors,ignored=ignored,
                loss_percent=100*(sent-received)/sent if sent else 100,
                missed_scheduler_slots=missed,maximum_pending=peak,maximum_reply_gap_ms=max_gap_ns/1e6,
                median_ms=statistics.median(rtts) if rtts else None,p95_ms=percentile(rtts),maximum_ms=max(rtts) if rtts else None,
                adjacent_rtt_delta_p95_ms=percentile(variations),probe_hz=50,payload_bytes=128,
                gaming_gate_passed=gaming_pass(sent,received,rtts,variations,missed))
    with Path('/out/'+stem+'.json').open('x') as f:json.dump(result,f,indent=2)
    print(json.dumps(result),flush=True)


if __name__=='__main__':
    p=argparse.ArgumentParser();p.add_argument('mode',choices=['server','client']);p.add_argument('--seconds',type=int,default=60);p.add_argument('--stem',default='game-udp');a=p.parse_args()
    assert 1<=a.seconds<=2100 and re.fullmatch(r'[A-Za-z0-9_-]{1,48}',a.stem)
    server() if a.mode=='server' else client(a.seconds,a.stem)
