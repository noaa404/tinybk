import argparse, datetime, json, os, platform, shutil, statistics, subprocess, time, urllib.parse, urllib.request
from pathlib import Path

from fixtures.server import FixtureServer

# Generated run files live in --workdir. Binary paths and the pi/model are CLI flags.
BASE = Path(os.environ.get('BENCH_WORKDIR', '/tmp/tinybk-benchmark-run'))
TINYBK = os.environ.get('TINYBK_BIN', '')
PINCHTAB = os.environ.get('PINCHTAB_BIN', '')
PI = os.environ.get('PI_BIN', '')
CHROMIUM = os.environ.get('CHROMIUM_BIN', '/usr/bin/chromium')
FIXTURE_PORT = 39002
CDP_PORT = 39001
PINCHTAB_PORT = 9867
FIXTURE = f'http://127.0.0.1:{FIXTURE_PORT}'
CDP = f'http://127.0.0.1:{CDP_PORT}'

PACKAGE_DIR = Path(__file__).resolve().parent
FIXTURES_DIR = PACKAGE_DIR / 'fixtures'
PAGES_DIR = FIXTURES_DIR / 'pages'
TASKS = json.loads((FIXTURES_DIR / 'tasks.json').read_text())

GUIDES = {
    'tinybk': '''Browser command: ./browser
Commands:
  ./browser goto <url>
  ./browser see
  ./browser map
  ./browser click <css|ref=@id|label=text>
  ./browser type <css|ref=@id> <text>
''',
    'pinchtab': '''Browser command: ./browser
Commands:
  ./browser nav <url> --text
  ./browser text
  ./browser snap -i -c
  ./browser click <ref> --text
  ./browser click --css <selector> --text
  ./browser fill <selector> <text>
''',
}

COMMON = '''You are completing one browser task. Use the bash tool only to run ./browser.
Do not use curl, wget, python, node, or any other network client.
Do not inspect local files. Use only the browser command below.
Lane commands:
{guide}
Task:
{prompt}
'''
def wait_http(url, timeout=20):
    deadline = time.time() + timeout
    while time.time() < deadline:
        try:
            with urllib.request.urlopen(url, timeout=1):
                return True
        except Exception:
            time.sleep(0.1)
    return False

def run_cmd(cmd, env=None, timeout=30, cwd=None):
    t0 = time.perf_counter()
    try:
        p = subprocess.run(cmd, env=env, cwd=cwd, stdin=subprocess.DEVNULL, capture_output=True, text=True, timeout=timeout)
        return (time.perf_counter()-t0)*1000, p.returncode, p.stdout, p.stderr
    except subprocess.TimeoutExpired:
        return (time.perf_counter()-t0)*1000, 124, '', 'timeout'

def parse_usage(lines):
    total = {'input':0,'output':0,'cacheRead':0,'cacheWrite':0,'reasoning':0,'totalTokens':0,'cost':0.0}
    api_calls = 0
    tool_calls = 0
    final_text = ''
    bash_commands = []
    for line in lines:
        try: obj = json.loads(line)
        except Exception: continue
        typ = obj.get('type')
        if typ == 'turn_end':
            api_calls += 1
            u = (obj.get('message') or {}).get('usage') or {}
            for k in ['input','output','cacheRead','cacheWrite','reasoning','totalTokens']:
                total[k] += int(u.get(k) or 0)
            total['cost'] += float((u.get('cost') or {}).get('total') or 0)
        elif typ == 'tool_execution_start':
            tool_calls += 1
        elif typ == 'agent_end':
            for m in obj.get('messages') or []:
                if m.get('role') == 'assistant':
                    for c in m.get('content') or []:
                        if c.get('type') == 'text':
                            final_text = c.get('text','')
                        elif c.get('type') == 'toolCall' and c.get('name') == 'bash':
                            args = c.get('arguments') or {}
                            bash_commands.append(str(args.get('command','')))
    return total, api_calls, tool_calls, final_text.strip(), bash_commands

def percentile(xs, p):
    if not xs: return 0
    xs = sorted(xs)
    i = max(0, min(len(xs)-1, int(__import__('math').ceil(p*len(xs))-1)))
    return xs[i]

def main():
    global BASE, TINYBK, PINCHTAB, PI, CHROMIUM
    ap = argparse.ArgumentParser(description='Real-agent-loop MVP benchmark for tinybk vs PinchTab')
    ap.add_argument('--tinybk', default=os.environ.get('TINYBK_BIN', ''), help='tinybk binary')
    ap.add_argument('--pinchtab', default=os.environ.get('PINCHTAB_BIN', ''), help='PinchTab binary')
    ap.add_argument('--pi', default=os.environ.get('PI_BIN', ''), help='pi binary')
    ap.add_argument('--chromium', default=os.environ.get('CHROMIUM_BIN', '/usr/bin/chromium'), help='Chromium binary')
    ap.add_argument('--provider', default='deepseek')
    ap.add_argument('--model', default='deepseek-flash')
    ap.add_argument('--reps', type=int, default=3)
    ap.add_argument('--tasks', default=','.join(TASKS))
    ap.add_argument('--lanes', default='tinybk,pinchtab')
    ap.add_argument('--workdir', default=str(BASE))
    ap.add_argument('--out', default='results.json', help='Result JSON path; relative paths are under --workdir')
    args = ap.parse_args()
    if not args.tinybk or not args.pinchtab or not args.pi:
        ap.error('--tinybk, --pinchtab, and --pi are required (or set TINYBK_BIN, PINCHTAB_BIN, PI_BIN)')
    BASE = Path(args.workdir).expanduser().resolve()
    TINYBK, PINCHTAB, PI, CHROMIUM = args.tinybk, args.pinchtab, args.pi, args.chromium
    task_names = [x.strip() for x in args.tasks.split(',') if x.strip()]
    lanes = [x.strip() for x in args.lanes.split(',') if x.strip()]
    removed = [t for t in task_names if t not in TASKS] + [l for l in lanes if l not in GUIDES]
    if removed:
        ap.error('unknown task/lane: ' + ', '.join(removed))
    BASE.mkdir(parents=True, exist_ok=True)
    results_dir = BASE / 'runs'
    shutil.rmtree(results_dir, ignore_errors=True)
    results_dir.mkdir(parents=True)
    fixture = FixtureServer(FIXTURE_PORT, PAGES_DIR)
    fixture.start()
    chrome = None
    pt_server = None
    pt_env = os.environ.copy()
    pt_home = BASE / 'pinchtab-home'
    shutil.rmtree(pt_home, ignore_errors=True)
    pt_home.mkdir(parents=True)
    pt_env['HOME'] = str(pt_home)
    pt_env['PINCHTAB_CONFIG'] = str(pt_home / '.pinchtab' / 'config.json')
    pt_env['PI_TELEMETRY'] = '0'
    pi_env = os.environ.copy()
    pi_env['PATH'] = str(Path(PI).parent) + ':' + pi_env.get('PATH','')
    pi_env['PI_TELEMETRY'] = '0'
    session = ''
    try:
        chrome_dir = BASE / 'chrome-profile'
        shutil.rmtree(chrome_dir, ignore_errors=True)
        chrome = subprocess.Popen([
            CHROMIUM, '--headless=new', f'--remote-debugging-port={CDP_PORT}',
            f'--user-data-dir={chrome_dir}', '--no-first-run', '--no-default-browser-check',
            '--disable-backgrounding-occluded-windows', '--disable-renderer-backgrounding',
            '--disable-background-timer-throttling', '--window-size=1280,900', 'about:blank'
        ], stdout=open(BASE/'chrome.log','wb'), stderr=subprocess.STDOUT)
        if not wait_http(CDP + '/json/version'): raise RuntimeError('tinybk chromium did not start')

        _, rc, out, err = run_cmd([PINCHTAB, 'server', 'stop'], env=pt_env)
        pt_server = subprocess.Popen([PINCHTAB, 'server', '--yolo'], env=pt_env,
            stdout=open(BASE/'pinchtab-server.log','wb'), stderr=subprocess.STDOUT)
        for _ in range(150):
            _, rc, _, _ = run_cmd([PINCHTAB, 'health'], env=pt_env, timeout=2)
            if rc == 0: break
            time.sleep(0.1)
        else:
            raise RuntimeError('pinchtab server did not become healthy')
        _, rc, out, err = run_cmd([PINCHTAB, 'session', 'create', '--agent-id', 'mvp-bench'], env=pt_env)
        if rc != 0: raise RuntimeError('pinchtab session create failed: ' + err)
        session = out.strip().splitlines()[0].strip()
        pt_env['PINCHTAB_SESSION'] = session

        _, rc, out, _ = run_cmd([TINYBK, '--cdp', CDP, 'tab'])
        tabs = json.loads(out).get('tabs', []) if rc == 0 else []
        if tabs:
            run_cmd([TINYBK, '--cdp', CDP, 'tab', 'activate', tabs[0]['id']])

        records = []
        for rep in range(1, args.reps+1):
            lane_order = lanes if rep % 2 == 1 else list(reversed(lanes))
            for task_name in task_names:
                task = TASKS[task_name]
                for lane in lane_order:
                    run_id = f'{lane}-{task_name}-r{rep}'
                    wd = results_dir / run_id
                    wd.mkdir(parents=True)
                    # reset and clear event state before each independent run
                    if lane == 'tinybk':
                        run_cmd([TINYBK, '--cdp', CDP, 'goto', FIXTURE + '/blank.html', '--text', '0'])
                        wrapper = f'''#!/bin/sh
printf '%s\\n' "$*" >> "$(dirname "$0")/commands.log"
exec {TINYBK} --cdp {CDP} "$@"
'''
                    else:
                        run_cmd([PINCHTAB, 'nav', FIXTURE + '/blank.html'], env=pt_env)
                        wrapper = f'''#!/bin/sh
printf '%s\\n' "$*" >> "$(dirname "$0")/commands.log"
HOME={pt_home} PINCHTAB_SESSION={session} exec {PINCHTAB} --server http://127.0.0.1:{PINCHTAB_PORT} "$@"
'''
                    time.sleep(0.25)
                    fixture.clear_events()
                    b = wd / 'browser'
                    b.write_text(wrapper)
                    b.chmod(0o755)
                    url = FIXTURE + task['path'] + '?run=' + urllib.parse.quote(run_id)
                    prompt = COMMON.format(guide=GUIDES[lane], prompt=task['prompt'].format(url=url))
                    t0 = time.perf_counter()
                    try:
                        p = subprocess.run([PI, '--provider',args.provider,'--model',args.model,'--mode','json',
                            '--no-session','--no-context-files','--no-skills','--no-extensions','--no-mcp',
                            '--no-prompt-templates','--no-themes','--tools','bash','--thinking','off','-p',prompt],
                            env=pi_env, cwd=wd, stdin=subprocess.DEVNULL, capture_output=True, text=True, timeout=240)
                        wall_ms = (time.perf_counter()-t0)*1000
                        out, err, rc = p.stdout, p.stderr, p.returncode
                    except subprocess.TimeoutExpired as e:
                        wall_ms = (time.perf_counter()-t0)*1000
                        out = (e.stdout or '')
                        if isinstance(out, bytes): out = out.decode(errors='replace')
                        err = 'timeout'
                        rc = 124
                    (wd/'out.jsonl').write_text(out)
                    (wd/'err.txt').write_text(err)
                    time.sleep(0.3)
                    events = [e for e in fixture.snapshot_events() if e.get('run') == run_id]
                    usage, api_calls, tool_calls, final_text, bash_commands = parse_usage(out.splitlines())
                    commands_file = wd / 'commands.log'
                    browser_commands = commands_file.read_text().splitlines() if commands_file.exists() else []
                    suspicious = [c for c in bash_commands if './browser' not in c]
                    event_ok = True
                    for req in task.get('required_events', []):
                        matches = [e for e in events if e.get('event') == req.get('event')]
                        fields = req.get('fields', {})
                        if not any(all(e.get(k) == v for k, v in fields.items()) for e in matches):
                            event_ok = False
                            break
                    if event_ok:
                        for forbidden in task.get('forbidden_events', []):
                            if any(e.get('event') == forbidden for e in events):
                                event_ok = False
                                break
                    answer_ok = task['expected'].lower() in final_text.lower()
                    record = {
                        'lane': lane, 'task': task_name, 'rep': rep, 'run_id': run_id,
                        'rc': rc, 'success': rc == 0 and event_ok and answer_ok,
                        'event_ok': event_ok, 'answer_ok': answer_ok,
                        'final_answer': final_text,
                        'usage': usage, 'api_calls': api_calls, 'tool_calls': tool_calls,
                        'browser_commands': browser_commands,
                        'suspicious_commands': suspicious,
                        'wall_ms': round(wall_ms, 2),
                        'events': events,
                    }
                    (wd/'result.json').write_text(json.dumps(record, indent=2))
                    records.append(record)
                    print(json.dumps({k: record[k] for k in ['run_id','rc','success','event_ok','answer_ok','api_calls','tool_calls','wall_ms','final_answer']}), flush=True)
        summary = {}
        for lane in lanes:
            for task_name in task_names:
                rs = [r for r in records if r['lane']==lane and r['task']==task_name]
                if not rs: continue
                summary[f'{lane}/{task_name}'] = {
                    'n': len(rs),
                    'success_rate': sum(1 for r in rs if r['success'])/len(rs),
                    'avg_tokens': round(statistics.mean(r['usage']['totalTokens'] for r in rs), 1),
                    'median_tokens': round(statistics.median(r['usage']['totalTokens'] for r in rs), 1),
                    'avg_cost_usd': round(statistics.mean(r['usage']['cost'] for r in rs), 6),
                    'avg_api_calls': round(statistics.mean(r['api_calls'] for r in rs), 2),
                    'avg_tool_calls': round(statistics.mean(r['tool_calls'] for r in rs), 2),
                    'avg_wall_ms': round(statistics.mean(r['wall_ms'] for r in rs), 1),
                    'suspicious_runs': sum(1 for r in rs if r['suspicious_commands']),
                }
        _, _, tbver, _ = run_cmd([TINYBK, '--version'], timeout=10)
        _, _, ptver, _ = run_cmd([PINCHTAB, '--version'], timeout=10)
        _, _, piv, _ = run_cmd([PI, '--version'], timeout=10)
        try:
            with urllib.request.urlopen(CDP + '/json/version', timeout=5) as r:
                chrome_version = json.load(r).get('Browser', 'unknown')
        except Exception:
            chrome_version = 'unknown'
        metadata = {
            'benchmark': 'mvp-deepseek-flash-v1',
            'generated_at': datetime.datetime.now(datetime.timezone.utc).isoformat(),
            'tinybk_version': tbver.strip(),
            'pinchtab_version': ptver.strip(),
            'pi_version': piv.strip(),
            'provider': args.provider,
            'model': args.model,
            'thinking': 'off',
            'chrome': chrome_version,
            'platform': platform.platform(),
            'machine': platform.machine(),
            'reps': args.reps,
            'tasks': {name: TASKS[name] for name in task_names},
            'guides': {lane: GUIDES[lane] for lane in lanes},
        }
        report = {'metadata': metadata, 'summary': summary, 'runs': records}
        out_path = Path(args.out)
        if not out_path.is_absolute():
            out_path = BASE / out_path
        out_path.parent.mkdir(parents=True, exist_ok=True)
        out_path.write_text(json.dumps(report, indent=2))
        (BASE/'summary.json').write_text(json.dumps(summary, indent=2))
        print('\nSUMMARY')
        print(json.dumps(summary, indent=2))
        print(f'\nRESULT_FILE {out_path}')
    finally:
        try: run_cmd([PINCHTAB, 'server', 'stop'], env=pt_env, timeout=10)
        except Exception: pass
        if pt_server:
            pt_server.terminate()
        if chrome:
            chrome.terminate()
        fixture.stop()

if __name__ == '__main__':
    main()
