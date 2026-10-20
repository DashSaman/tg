"""Optional external receiver-read telemetry, not the iperf acceptance verdict.

Run inside the isolated exit namespace. Reads only iperf3 /proc I/O counters;
no packet capture, profiling, credentials, or changes to TG. PID separates trials.
Docker removal terminates this helper. Final acceptance still uses iperf JSON.
"""
import json
from pathlib import Path
import time

with open('/out/live-receiver-io.jsonl', 'a', buffering=1) as output:
    while True:
        for process in Path('/proc').iterdir():
            if not process.name.isdigit():
                continue
            try:
                if (process/'comm').read_text().strip() != 'iperf3':
                    continue
                values = dict(line.split(':', 1) for line in (process/'io').read_text().splitlines())
                output.write(json.dumps(dict(time=time.time(), pid=int(process.name), rchar=int(values['rchar'])))+'\n')
            except (FileNotFoundError, ProcessLookupError):
                pass
        time.sleep(1)
