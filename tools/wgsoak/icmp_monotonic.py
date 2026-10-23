"""Linux private WG lab ICMP observer. All RTT arithmetic uses MONOTONIC_NS."""
import argparse,json,math,os,re,select,socket,statistics,struct,time
from pathlib import Path

def checksum(data):
    if len(data)%2:data+=b'\0'
    total=sum(struct.unpack('!'+str(len(data)//2)+'H',data))
    while total>>16:total=(total&65535)+(total>>16)
    return ~total&65535

def packet(kind,identifier,sequence,cookie):
    payload=cookie+struct.pack('!Q',sequence)+bytes(32)
    body=struct.pack('!BBHHH',kind,0,0,identifier,sequence)+payload
    return body[:2]+struct.pack('!H',checksum(body))+body[4:]

def reply_sequence(data,identifier,cookie):
    if len(data)!=64 or checksum(data)!=0:return None
    kind,code,_,ident,seq=struct.unpack('!BBHHH',data[:8])
    if kind!=0 or code!=0 or ident!=identifier or data[8:]!=cookie+struct.pack('!Q',seq)+bytes(32):return None
    return seq

def observe(count,interval,timeout):
    info=time.get_clock_info('monotonic');assert info.monotonic and not info.adjustable
    sock=socket.socket(socket.AF_INET,socket.SOCK_DGRAM,socket.IPPROTO_ICMP)
    sock.setsockopt(socket.SOL_SOCKET,socket.SO_BINDTODEVICE,b'wg-tg\0')
    sock.bind(('10.77.0.1',0));sock.setblocking(False)
    ident=sock.getsockname()[1];cookie=os.urandom(16)
    begin=time.monotonic_ns();wall=time.time_ns();next_send=begin
    rows={};pending=set();sent=0;ignored=duplicates=late=0
    timeout_ns=int(timeout*1e9)
    try:
        while sent<count or pending:
            now=time.monotonic_ns()
            if sent<count and now>=next_send:
                seq=sent;before=time.monotonic_ns()
                row=dict(sequence=seq,send_monotonic_ns=before,status='pending',rtt_ms=None)
                rows[seq]=row
                try:
                    sock.sendto(packet(8,ident,seq,cookie),('10.77.0.2',0))
                    pending.add(seq)
                except OSError as e:row.update(status='send_error',error=type(e).__name__)
                sent+=1;next_send=before+int(interval*1e9)
            now=time.monotonic_ns()
            for seq in list(pending):
                if now-rows[seq]['send_monotonic_ns']>=timeout_ns:
                    rows[seq]['status']='timeout';pending.remove(seq)
            if sent>=count and not pending:break
            wait=.25
            if sent<count:wait=min(wait,max(0,(next_send-now)/1e9))
            if pending:wait=min(wait,max(0,min(rows[s]['send_monotonic_ns']+timeout_ns-now for s in pending)/1e9))
            if not select.select([sock],[],[],wait)[0]:continue
            while True:
                try:data,peer=sock.recvfrom(65535)
                except BlockingIOError:break
                received=time.monotonic_ns();seq=reply_sequence(data,ident,cookie)
                if seq is None or seq not in rows or peer[0]!='10.77.0.2':ignored+=1;continue
                row=rows[seq]
                if seq not in pending:
                    if row['status']=='received':duplicates+=1
                    else:late+=1
                    continue
                delta=received-row['send_monotonic_ns'];assert delta>=0
                if delta>=timeout_ns:row['status']='timeout';late+=1
                else:row.update(status='received',receive_monotonic_ns=received,rtt_ms=delta/1e6)
                pending.remove(seq)
    finally:sock.close()
    end=time.monotonic_ns();wall_end=time.time_ns()
    good=[r for r in rows.values() if r['status']=='received'];values=[r['rtt_ms'] for r in good]
    differences=[abs(b['rtt_ms']-a['rtt_ms']) for a,b in zip(good,good[1:]) if b['sequence']==a['sequence']+1]
    percentile=lambda xs:sorted(xs)[math.ceil(len(xs)*.95)-1] if xs else None
    result=dict(clock='CLOCK_MONOTONIC via time.monotonic_ns',clock_implementation=info.implementation,
        scope='Private namespace ICMP56-byte payload over actual kernelWG/TG. Observed user-space RTT includes scheduler delay; not one-way or RTP/UDP jitter. No wall-clock arithmetic in RTT.',
        requested=count,transmitted=sent,received=len(good),loss_percent=100*(sent-len(good))/sent,
        monotonic_elapsed_seconds=(end-begin)/1e9,wall_elapsed_seconds=(wall_end-wall)/1e9,
        wall_minus_monotonic_elapsed_seconds=((wall_end-wall)-(end-begin))/1e9,
        start_monotonic_ns=begin,end_monotonic_ns=end,start_wall_ns=wall,end_wall_ns=wall_end,
        interval_seconds=interval,reply_timeout_seconds=timeout,median_ms=statistics.median(values) if values else None,
        p95_ms=percentile(values),maximum_ms=max(values) if values else None,
        rtt_stddev_ms=statistics.pstdev(values) if values else None,adjacent_rtt_delta_p95_ms=percentile(differences),
        ignored=ignored,duplicates=duplicates,late_after_timeout=late,probes=list(rows.values()))
    result['numeric_latency_and_variation_passed']=bool(count>=60 and len(good)==count and result['median_ms']<20 and result['p95_ms']<30 and result['adjacent_rtt_delta_p95_ms'] is not None and result['adjacent_rtt_delta_p95_ms']<10)
    return result

if __name__=='__main__':
    p=argparse.ArgumentParser();p.add_argument('--count',type=int,default=300);p.add_argument('--interval',type=float,default=1);p.add_argument('--timeout',type=float,default=5);p.add_argument('--stem',default='ping-monotonic');a=p.parse_args()
    assert 1<=a.count<=1700 and .05<=a.interval<=5 and 1<=a.timeout<=15
    assert re.fullmatch(r'[A-Za-z0-9_-]{1,48}',a.stem)
    d=observe(a.count,a.interval,a.timeout)
    target=Path('/out')/(a.stem+'.json')
    with target.open('x') as f:json.dump(d,f,indent=2)
    print(json.dumps({k:v for k,v in d.items() if k!='probes'}),flush=True)
