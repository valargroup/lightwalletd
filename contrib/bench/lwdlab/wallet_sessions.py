#!/usr/bin/env python3
"""Run independent copies of disposable Vizor wallets against a private server."""
import argparse
from concurrent.futures import ThreadPoolExecutor
from datetime import datetime, timezone
import hashlib
import json
import math
import os
from pathlib import Path
import shutil
import signal
import subprocess
import sys
import threading
import time
from urllib.parse import urlsplit


def sha256(path):
    with Path(path).open('rb') as source:
        return hashlib.file_digest(source, 'sha256').hexdigest()


def write_json(path, value):
    Path(path).write_text(json.dumps(value, indent=2) + '\n')


def prepare(args):
    destination = Path(args.destination)
    destination.mkdir(mode=0o700, parents=True, exist_ok=False)
    build = json.loads(Path(args.build_manifest).read_text())
    if build['binary_sha256'] != sha256(args.wallet_binary):
        raise ValueError('wallet binary does not match its build manifest')
    wallets = []
    for index in range(args.count):
        directory = destination / f'{index:03d}'
        directory.mkdir(mode=0o700)
        with (directory / 'create.stdout').open('w') as out, (directory / 'create.stderr').open('w') as err:
            subprocess.run([args.wallet_binary, 'create', str(directory / 'wallet.db'),
                            str(args.birthday), '1'], stdout=out, stderr=err, check=True, timeout=60)
        wallets.append({'directory': directory.name, 'db_sha256': sha256(directory / 'wallet.db')})
    write_json(destination / 'fixture.json', {
        'schema': 1, 'kind': 'disposable-unfunded-vizor', 'created': datetime.now(timezone.utc).isoformat(),
        'birthday': args.birthday, 'wallet_build': build, 'wallets': wallets,
    })
    print(f'Created {len(wallets)} independent disposable wallets in {destination}')


def sync_one(binary, directory, endpoint, mode, timeout, gate, expected_tip):
    gate.wait()
    started = time.monotonic()
    started_wall = datetime.now(timezone.utc).isoformat()
    timed_out = False
    with (directory / 'sync.stdout').open('w') as out, (directory / 'sync.stderr').open('w') as err:
        process = subprocess.Popen([binary, 'sync', str(directory / 'wallet.db'), endpoint, str(mode)],
                                   stdout=out, stderr=err, start_new_session=True)
        # wait4 attributes CPU and peak RSS to this wallet, even with concurrent clients.
        while True:
            pid, status, usage = os.wait4(process.pid, os.WNOHANG)
            if pid:
                break
            if time.monotonic() - started > timeout:
                timed_out = True
                try:
                    os.killpg(process.pid, signal.SIGKILL)
                except ProcessLookupError:
                    pass
                _, status, usage = os.wait4(process.pid, 0)
                break
            time.sleep(0.05)
        process.returncode = os.waitstatus_to_exitcode(status)
    finished = time.monotonic()
    records = []
    for line in (directory / 'sync.stdout').read_text().splitlines():
        try:
            record = json.loads(line)
            if isinstance(record, dict):
                records.append(record)
        except json.JSONDecodeError:
            continue
    progress = next((record for record in reversed(records) if 'is_complete' in record), {})
    final = records[-1] if records else {}
    success = (process.returncode == 0 and not timed_out and final.get('success') is True
               and final.get('operation') == 'sync' and progress.get('is_complete') is True
               and progress.get('chain_tip_height') == expected_tip)
    result = {
        'wallet': directory.name, 'started': started_wall,
        'elapsed_seconds': finished - started, 'exit_code': process.returncode,
        'timed_out': timed_out, 'success': success, 'progress': progress,
        'user_cpu_seconds': usage.ru_utime, 'system_cpu_seconds': usage.ru_stime,
        'peak_rss_bytes': usage.ru_maxrss * (1 if sys.platform == 'darwin' else 1024),
    }
    write_json(directory / 'result.json', result)
    return result, started, finished


def run(args):
    endpoint = urlsplit(args.url)
    if endpoint.scheme not in ('http', 'https') or endpoint.hostname not in ('127.0.0.1', '::1', 'localhost'):
        raise ValueError('wallet sessions require a loopback server or SSH tunnel')
    source = Path(args.fixture_dir)
    manifest = json.loads((source / 'fixture.json').read_text())
    if manifest.get('kind') != 'disposable-unfunded-vizor':
        raise ValueError('expected a disposable fixture created by this tool')
    if sha256(args.wallet_binary) != manifest['wallet_build']['binary_sha256']:
        raise ValueError('wallet binary changed since fixture creation')
    if len(manifest['wallets']) < args.clients:
        raise ValueError('not enough independent wallet fixtures')
    output = Path(args.output)
    output.mkdir(mode=0o700, parents=True, exist_ok=False)
    directories = []
    for entry in manifest['wallets'][:args.clients]:
        name = entry['directory']
        if Path(name).name != name or name in ('.', '..'):
            raise ValueError('invalid fixture directory')
        original = source / name
        if sha256(original / 'wallet.db') != entry['db_sha256']:
            raise ValueError(f'wallet fixture {name} changed')
        directory = output / name
        shutil.copytree(original, directory)
        directories.append(directory)
    gate = threading.Barrier(args.clients)
    with ThreadPoolExecutor(max_workers=args.clients) as workers:
        futures = [workers.submit(sync_one, args.wallet_binary, directory, args.url, args.mode,
                                  args.timeout, gate, args.expected_tip) for directory in directories]
        completed = [future.result() for future in futures]
    clients = [result[0] for result in completed]
    durations = sorted(client['elapsed_seconds'] for client in clients if client['success'])
    result = {
        'schema': 1, 'label': args.label, 'clients': args.clients, 'mode': args.mode,
        'fixture': manifest, 'expected_tip': args.expected_tip,
        'client_host': {'platform': sys.platform, 'machine': os.uname().machine, 'logical_cpus': os.cpu_count()},
        'all_complete': all(client['success'] for client in clients),
        'completed_clients': len(durations),
        'batch_elapsed_seconds': max(item[2] for item in completed) - min(item[1] for item in completed),
        'wallet_results': clients,
    }
    if durations:
        result['wallet_seconds_p50'] = durations[math.ceil(0.5 * len(durations)) - 1]
        result['wallet_seconds_p95'] = durations[math.ceil(0.95 * len(durations)) - 1]
    write_json(output / 'result.json', result)
    print(json.dumps({key: result[key] for key in ('label', 'clients', 'completed_clients', 'all_complete', 'batch_elapsed_seconds')}))
    if not result['all_complete']:
        raise SystemExit(1)


def positive(value):
    value = int(value)
    if value < 1:
        raise argparse.ArgumentTypeError('must be positive')
    return value


def main():
    os.umask(0o077)
    parser = argparse.ArgumentParser(description=__doc__)
    sub = parser.add_subparsers(dest='operation', required=True)
    prepare_parser = sub.add_parser('prepare')
    prepare_parser.add_argument('--wallet-binary', required=True)
    prepare_parser.add_argument('--build-manifest', required=True)
    prepare_parser.add_argument('--destination', required=True)
    prepare_parser.add_argument('--birthday', type=positive, required=True)
    prepare_parser.add_argument('--count', type=positive, default=32)
    run_parser = sub.add_parser('run')
    run_parser.add_argument('--wallet-binary', required=True)
    run_parser.add_argument('--fixture-dir', required=True)
    run_parser.add_argument('--output', required=True)
    run_parser.add_argument('--label', required=True)
    run_parser.add_argument('--clients', type=positive, required=True)
    run_parser.add_argument('--expected-tip', type=positive, required=True)
    run_parser.add_argument('--mode', type=int, choices=(1, 2), default=1)
    run_parser.add_argument('--timeout', type=positive, default=1800)
    run_parser.add_argument('--url', default='http://127.0.0.1:19068')
    args = parser.parse_args()
    args.wallet_binary = str(Path(args.wallet_binary).resolve(strict=True))
    (prepare if args.operation == 'prepare' else run)(args)


if __name__ == '__main__':
    main()
