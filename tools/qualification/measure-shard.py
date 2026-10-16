#!/usr/bin/env python3
"""Run only local browser fixtures and measure their actual process/cgroup use."""
import json
import os
from pathlib import Path
import subprocess
import sys
import threading
import time


def cgroup_cpu_peak(samples):
    peak = 0.0
    for previous, current in zip(samples, samples[1:]):
        elapsed = current['elapsed_seconds'] - previous['elapsed_seconds']
        before = previous.get('cgroup_cpu', {}).get('usage_usec')
        after = current.get('cgroup_cpu', {}).get('usage_usec')
        if elapsed > 0 and before is not None and after is not None:
            peak = max(peak, (int(after) - int(before)) / 1e6 / elapsed)
    return peak


ROOT = Path(__file__).resolve().parents[2]
ARTIFACTS = Path(os.environ.get('TSW_QUALIFICATION_ARTIFACTS', '/tmp/tsw-account-environment/shard-followup'))
PHASE, COUNT = sys.argv[1], int(sys.argv[2])
STORAGE_ONLY = len(sys.argv) > 3 and sys.argv[3] == 'storage'
assert COUNT in (1, 5)
assert PHASE.replace('-', '').isalnum()
EVIDENCE = ARTIFACTS / 'runtime-evidence'
NAME = 'tsw-shard-qualification-' + PHASE
MEMORY_LIMIT = '2g' if COUNT == 1 else '3g'
COMMAND = [
    'docker', 'run', '--rm', '--name', NAME, '--network', 'none',
    '--hostname', NAME, '--shm-size', '256m', '--memory', MEMORY_LIMIT,
    '--memory-swap', MEMORY_LIMIT, '--pids-limit', '512' if COUNT == 1 else '1024',
    '--entrypoint', '/usr/bin/tini',
    '--mount', f'type=bind,src={ARTIFACTS / "engine"},dst=/engine,readonly',
    '--mount', f'type=bind,src={ARTIFACTS / "qualification.test"},dst=/qualification.test,readonly',
    '--mount', f'type=bind,src={ROOT / "tools/qualification/run-shard-fixtures.sh"},dst=/run-fixtures.sh,readonly',
    '--mount', f'type=bind,src={EVIDENCE},dst=/evidence',
    '-e', 'TSW_CHROMIUM_PATH=/engine/ShardX-Linux/chrome',
    '-e', 'TSW_BROWSER_EVIDENCE=/evidence', '-e', 'TSW_QUALIFICATION_EXTENDED=1',
    '-e', 'TSW_QUALIFICATION_SWIFTSHADER=1', '-e', f'TSW_QUALIFICATION_PHASE={PHASE}',
    '-e', f'TSW_QUALIFICATION_CONCURRENCY={COUNT}',
    '-e', f'TSW_QUALIFICATION_STORAGE_ONLY={int(STORAGE_ONLY)}',
    'apophis-teamseatwatch:mother-delete-6ed5e36', '-g', '--',
    'xvfb-run', '-a', '/run-fixtures.sh',
]
started = time.monotonic()
process = subprocess.Popen(COMMAND, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True)
done = threading.Event()
samples = []
errors = []
ticks = os.sysconf('SC_CLK_TCK')
page_size = os.sysconf('SC_PAGE_SIZE')


def sample():
    group = None
    cpu_previous = {}
    cpu_ticks_total = 0
    while not done.is_set():
        if group is None:
            inspected = subprocess.run(['docker', 'inspect', '--format', '{{.State.Pid}}', NAME], capture_output=True, text=True)
            try:
                pid = int(inspected.stdout.strip())
                group = (Path('/proc') / str(pid) / 'cgroup').read_text().split('0::', 1)[1].strip()
            except (ValueError, OSError, IndexError):
                time.sleep(0.1)
                continue
        browser = []
        for entry in Path('/proc').iterdir():
            if not entry.name.isdigit():
                continue
            try:
                if group not in (entry / 'cgroup').read_text():
                    continue
                argv = (entry / 'cmdline').read_bytes()
                if str((entry / 'exe').readlink()) != '/engine/ShardX-Linux/chrome':
                    continue
                stat = (entry / 'stat').read_text().rsplit(') ', 1)[1].split()
                if stat[0] == 'Z':
                    continue
                key = (int(entry.name), stat[19])
                current_ticks = int(stat[11]) + int(stat[12])
                cpu_ticks_total += max(0, current_ticks - cpu_previous.get(key, 0))
                cpu_previous[key] = current_ticks
                browser.append({'pid': int(entry.name), 'parent': int(stat[1]), 'start_ticks': int(stat[19]), 'exe': '/engine/ShardX-Linux/chrome', 'argv': [item.decode(errors='replace') for item in argv.split(b'\0') if item], 'rss_bytes': int(stat[21]) * page_size, 'main_candidate': b'--type=' not in argv})
            except (OSError, ValueError, IndexError):
                continue
        browser_pids = {p['pid'] for p in browser}
        for child in browser:
            child['main'] = child['main_candidate'] and child['parent'] not in browser_pids
        point = {'elapsed_seconds': time.monotonic() - started, 'chrome_rss_sum_bytes': sum(p['rss_bytes'] for p in browser), 'chrome_processes': len(browser), 'chrome_main_processes': sum(p['main'] for p in browser), 'chrome_main_candidates': sum(p['main_candidate'] for p in browser), 'chrome_cpu_seconds': cpu_ticks_total / ticks, 'browser_processes': browser}
        cg = Path('/sys/fs/cgroup') / group.lstrip('/')
        try:
            point['cgroup_memory_current_bytes'] = int((cg / 'memory.current').read_text())
            point['cgroup_memory_peak_bytes'] = int((cg / 'memory.peak').read_text())
            point['cgroup_cpu'] = dict(line.split() for line in (cg / 'cpu.stat').read_text().splitlines())
            point['cgroup_memory_events'] = dict(line.split() for line in (cg / 'memory.events').read_text().splitlines())
            point['cgroup_pids_current'] = int((cg / 'pids.current').read_text())
        except OSError as error:
            if cg.exists():
                errors.append(str(error))
        samples.append(point)
        done.wait(0.1)


thread = threading.Thread(target=sample, daemon=True)
thread.start()
try:
    with (EVIDENCE / (PHASE + '-container.log')).open('w') as log:
        for line in process.stdout:
            print(line, end='', flush=True)
            log.write(line)
    code = process.wait()
finally:
    done.set()
    thread.join(timeout=5)
    if process.poll() is None:
        subprocess.run(['docker', 'rm', '-f', NAME], capture_output=True)
        process.wait(timeout=10)

elapsed = time.monotonic() - started
summary = {
    'phase': PHASE, 'requested_concurrency': COUNT, 'storage_only': STORAGE_ONLY,
    'command': COMMAND, 'elapsed_seconds': elapsed, 'exit_status': code,
    'sample_interval_seconds': 0.1, 'samples': len(samples),
    'chrome_process_tree_rss_sum_peak_bytes': max((p['chrome_rss_sum_bytes'] for p in samples), default=0),
    'chrome_process_count_peak': max((p['chrome_processes'] for p in samples), default=0),
    'chrome_main_process_count_peak': max((p['chrome_main_processes'] for p in samples), default=0),
    'chrome_main_candidate_count_peak': max((p['chrome_main_candidates'] for p in samples), default=0),
    'chrome_cpu_seconds': max((p['chrome_cpu_seconds'] for p in samples), default=0),
    'cgroup_memory_peak_bytes': max((p.get('cgroup_memory_peak_bytes', 0) for p in samples), default=0),
    'cgroup_pids_peak': max((p.get('cgroup_pids_current', 0) for p in samples), default=0),
    'cgroup_cpu_usage_seconds': max((int(p.get('cgroup_cpu', {}).get('usage_usec', '0')) / 1e6 for p in samples), default=0),
    'cgroup_cpu_sample_interval_peak_cores': cgroup_cpu_peak(samples),
    'cgroup_oom_kill_count': max((int(p.get('cgroup_memory_events', {}).get('oom_kill', '0')) for p in samples), default=0),
    'profile_files_bytes': sum(p.stat().st_size for p in (EVIDENCE / 'profiles').rglob('*') if p.is_file()),
    'sampling_errors': sorted(set(errors)),
    'rss_caution': 'Per-process RSS includes shared pages repeatedly; cgroup memory also includes charged file cache. Neither is a real-site workload capacity prediction.',
}
logs = sorted(EVIDENCE.glob(PHASE + '-fixture-*.log'))
summary['tests'] = {'executed': len(logs), 'passed': sum('--- PASS:' in p.read_text() for p in logs), 'failed': sum('--- FAIL:' in p.read_text() for p in logs), 'incomplete': sum('--- PASS:' not in p.read_text() and '--- FAIL:' not in p.read_text() for p in logs)}
(EVIDENCE / (PHASE + '-resource-samples.json')).write_text(json.dumps(samples, indent=2))
(EVIDENCE / (PHASE + '-resources.json')).write_text(json.dumps(summary, indent=2))
print(json.dumps(summary, indent=2))
print('# tests', summary['tests']['executed'])
print('# pass', summary['tests']['passed'])
print('# fail', summary['tests']['failed'])
sys.exit(code)
