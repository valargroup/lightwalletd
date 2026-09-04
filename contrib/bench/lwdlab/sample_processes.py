#!/usr/bin/env python3
"""Sample Linux process CPU, RSS and I/O without querying wallet or node RPCs."""
import argparse
import json
import os
from pathlib import Path
import signal
import time


def snapshot(pid):
    directory = Path('/proc') / str(pid)
    # The comm field can contain spaces and parentheses. Fields after its final
    # parenthesis begin with state (field 3 in proc_pid_stat).
    fields = (directory / 'stat').read_text().rsplit(')', 1)[1].split()
    ticks = os.sysconf('SC_CLK_TCK')
    result = {
        'pid': pid, 'start_ticks': int(fields[19]),
        'user_cpu_seconds': int(fields[11]) / ticks,
        'system_cpu_seconds': int(fields[12]) / ticks,
        'rss_bytes': int(fields[21]) * os.sysconf('SC_PAGE_SIZE'),
        'threads': int(fields[17]),
    }
    result['io'] = {key: int(value.strip()) for key, value in
                    (line.split(':', 1) for line in (directory / 'io').read_text().splitlines())}
    return result


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--process', action='append', required=True, help='label=pid; may be repeated')
    parser.add_argument('--output', required=True, help='new JSONL file')
    parser.add_argument('--interval', type=float, default=0.25)
    parser.add_argument('--seconds', type=float, default=3600)
    args = parser.parse_args()
    if args.interval <= 0 or args.seconds <= 0:
        parser.error('interval and seconds must be positive')
    processes = {}
    for pair in args.process:
        name, pid = pair.split('=', 1)
        if name in processes or int(pid) < 1:
            parser.error('process labels must be unique and PIDs positive')
        processes[name] = int(pid)
    identities = {name: snapshot(pid)['start_ticks'] for name, pid in processes.items()}
    stopped = False

    def stop(_signal, _frame):
        nonlocal stopped
        stopped = True

    signal.signal(signal.SIGTERM, stop)
    signal.signal(signal.SIGINT, stop)
    started = time.monotonic()
    os.umask(0o077)
    with open(args.output, 'x') as output:
        while True:
            record = {'unix_seconds': time.time(), 'elapsed_seconds': time.monotonic() - started,
                      'processes': {}}
            for name, pid in processes.items():
                try:
                    value = snapshot(pid)
                    if value['start_ticks'] != identities[name]:
                        raise RuntimeError('PID reused by another process')
                    record['processes'][name] = value
                except (OSError, RuntimeError) as error:
                    record['processes'][name] = {'error': str(error)}
                    stopped = True
            output.write(json.dumps(record) + '\n')
            output.flush()
            if stopped or time.monotonic() - started >= args.seconds:
                break
            time.sleep(args.interval)
    if any('error' in item for item in record['processes'].values()):
        raise SystemExit(1)


if __name__ == '__main__':
    main()
