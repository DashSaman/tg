"""Build a disposable lab profiler, not a release binary or public pprof endpoint.

Run from any directory: python tools/wgsoak/profile.py --out /absolute/output
Then pass its tg-profile binary to run.py with --profile. Profiles cover the first
25 seconds from daemon start (including setup), not an entire 30-second trial.
Only a QUIC buffer_full counter is retained; qlog events/payloads are not logged.
Requires Docker and the project's cached Go module/build volumes.
"""
import argparse
import hashlib
import json
from pathlib import Path
import shutil
import subprocess
import tempfile


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--out', type=Path, required=True)
    args = parser.parse_args()
    source = Path(__file__).resolve().parents[2]
    out = args.out.resolve()
    out.mkdir(parents=True, exist_ok=False)
    hashes = {str(p.relative_to(source)).replace('\\', '/'): hashlib.sha256(p.read_bytes()).hexdigest()
              for p in source.rglob('*.go') if 'receipts' not in p.parts}
    with tempfile.TemporaryDirectory(prefix='tg-profile-') as temporary:
        root = Path(temporary)/'src'
        shutil.copytree(source, root, ignore=shutil.ignore_patterns('.git', 'receipts', '__pycache__'))
        main_file = root/'cmd/tg/main.go'
        text = main_file.read_text().replace('"context"', '"context"\n"runtime/pprof"\n"time"')
        text = text.replace('func main() {', '''func main() {
 if path := os.Getenv("TG_DIAGNOSTIC_PROFILE"); path != "" && len(os.Args)>1 && os.Args[1]=="run" {
  f, err := os.Create(path+".cpu.pprof"); if err != nil { panic(err) }
  if err := pprof.StartCPUProfile(f); err != nil { panic(err) }
  time.AfterFunc(25*time.Second, func() {
   pprof.StopCPUProfile(); f.Close()
   m,err:=os.Create(path+".heap.pprof");if err==nil {pprof.WriteHeapProfile(m);m.Close()}
  })
 }
''')
        main_file.write_text(text)
        quic = root/'internal/transport/quic.go'
        quic.write_text(quic.read_text().replace('EnableDatagrams: true,', 'EnableDatagrams: true, Tracer: diagnosticTracer,'))
        (quic.parent/'diagnostic_trace.go').write_text('''package transport
import("context";"os";"fmt";"time";"sync";"sync/atomic";"github.com/quic-go/quic-go";"github.com/quic-go/quic-go/qlog";"github.com/quic-go/quic-go/qlogwriter")
var diagOnce sync.Once
var diagDrops atomic.Uint64
type diagTrace struct{}
func(diagTrace) SupportsSchemas(string)bool{return true}
func(diagTrace) AddProducer()qlogwriter.Recorder{return diagTrace{}}
func(diagTrace) Close()error{return nil}
func(diagTrace) RecordEvent(e qlogwriter.Event){if d,ok:=e.(qlog.DatagramDropped);ok&&d.Trigger=="buffer_full"{diagDrops.Add(1)}}
func diagnosticTracer(context.Context,bool,quic.ConnectionID)qlogwriter.Trace{
 diagOnce.Do(func(){time.AfterFunc(25*time.Second,func(){os.WriteFile(os.Getenv("TG_DIAGNOSTIC_PROFILE")+".drops.txt",[]byte(fmt.Sprint(diagDrops.Load())),0600)})});return diagTrace{}
}
''')
        subprocess.run(['docker', 'run', '--rm', '--mount', f'type=bind,source={root},target=/src',
                        '--mount', f'type=bind,source={out},target=/out',
                        '--mount', 'type=volume,source=tg-go-cache,target=/root/.cache/go-build',
                        '--mount', 'type=volume,source=tg-go-mod,target=/go/pkg/mod', '-w', '/src',
                        '-e', 'GOTOOLCHAIN=local', 'golang:1.26-bookworm', 'go', 'build', '-o', '/out/tg-profile', './cmd/tg'], check=True)
    (out/'profile-source.json').write_text(json.dumps(dict(scope='instrumented disposable source copy; not release',
        source_go_sha256=hashes, binary_sha256=hashlib.sha256((out/'tg-profile').read_bytes()).hexdigest()), indent=2))


if __name__ == '__main__':
    main()
