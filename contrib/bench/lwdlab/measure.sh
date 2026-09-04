#!/usr/bin/env bash

set -euo pipefail

label=""
server=""
lab=""
data_dir=""
output_dir=""
warm_iterations=1
nocache=false
grpc_addr="127.0.0.1:19067"
metrics_url="http://127.0.0.1:19068/metrics"
backend_admin="http://127.0.0.1:18233"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --label) label="$2"; shift 2 ;;
    --server) server="$2"; shift 2 ;;
    --lab) lab="$2"; shift 2 ;;
    --data-dir) data_dir="$2"; shift 2 ;;
    --output-dir) output_dir="$2"; shift 2 ;;
    --warm-iterations) warm_iterations="$2"; shift 2 ;;
    --nocache) nocache=true; shift ;;
    --) shift; break ;;
    *) echo "unknown measure option: $1" >&2; exit 2 ;;
  esac
done

if [[ -z "$label" || -z "$server" || -z "$lab" || -z "$data_dir" || -z "$output_dir" || $# -eq 0 ]]; then
  echo "usage: measure.sh --label NAME --server PATH --lab PATH --data-dir PATH --output-dir PATH [--warm-iterations N] [--nocache] -- LOAD_FLAGS" >&2
  exit 2
fi

mkdir -p "$output_dir"
server_log="$output_dir/$label-server.log"
console_log="$output_dir/$label-console.log"
start_metrics="$output_dir/$label-start.metrics"
end_metrics="$output_dir/$label-end.metrics"

server_args=(
  --no-tls-very-insecure
  --no-backend-check
  --grpc-bind-addr "$grpc_addr"
  --http-bind-addr "127.0.0.1:19068"
  --log-file "$server_log"
  --log-level 2
  --data-dir "$data_dir"
  --rpcuser bench
  --rpcpassword bench
  --rpchost 127.0.0.1
  --rpcport 18232
)
if [[ "$nocache" == true ]]; then
  server_args+=(--nocache)
fi

"$server" "${server_args[@]}" >"$console_log" 2>&1 &
server_pid=$!
cleanup() {
  if kill -0 "$server_pid" 2>/dev/null; then
    kill -TERM "$server_pid" 2>/dev/null || true
    wait "$server_pid" 2>/dev/null || true
  fi
}
trap cleanup EXIT

ready=false
for _ in $(seq 1 100); do
  if curl -fsS "$metrics_url" >/dev/null 2>&1; then
    ready=true
    break
  fi
  if ! kill -0 "$server_pid" 2>/dev/null; then
    tail -50 "$console_log" >&2 || true
    tail -50 "$server_log" >&2 || true
    exit 1
  fi
  sleep 0.1
done
if [[ "$ready" != true ]]; then
  echo "lightwalletd did not become ready" >&2
  exit 1
fi

"$lab" load -address "$grpc_addr" "$@" -concurrency 1 -iterations "$warm_iterations" >/dev/null
curl -fsS --retry 10 --retry-all-errors --retry-delay 1 -X POST "$backend_admin/reset" >/dev/null
curl -fsS "$metrics_url" >"$start_metrics"
read -r start_cpu start_rss < <(
  ps -p "$server_pid" -o utime= -o stime= -o rss= |
    awk '{ split($1, u, ":"); split($2, s, ":"); print u[1] * 60 + u[2] + s[1] * 60 + s[2], $3 * 1024 }'
)

load_result=$("$lab" load -address "$grpc_addr" "$@")

read -r end_cpu end_rss < <(
  ps -p "$server_pid" -o utime= -o stime= -o rss= |
    awk '{ split($1, u, ":"); split($2, s, ":"); print u[1] * 60 + u[2] + s[1] * 60 + s[2], $3 * 1024 }'
)
curl -fsS --retry 5 --retry-all-errors --retry-delay 1 "$metrics_url" >"$end_metrics"
backend_result=$(curl -fsS --retry 10 --retry-all-errors --retry-delay 1 "$backend_admin/stats")

metric() {
  local file="$1"
  local name="$2"
  awk -v metric_name="$name" '$1 == metric_name { print $2; exit }' "$file"
}

start_alloc=$(metric "$start_metrics" go_memstats_alloc_bytes_total)
end_alloc=$(metric "$end_metrics" go_memstats_alloc_bytes_total)
start_mallocs=$(metric "$start_metrics" go_memstats_mallocs_total)
end_mallocs=$(metric "$end_metrics" go_memstats_mallocs_total)
start_gc=$(metric "$start_metrics" go_gc_duration_seconds_count)
end_gc=$(metric "$end_metrics" go_gc_duration_seconds_count)
end_goroutines=$(metric "$end_metrics" go_goroutines)

jq -cn \
  --arg label "$label" \
  --argjson load "$load_result" \
  --argjson backend "$backend_result" \
  --arg start_cpu "${start_cpu:-0}" \
  --arg end_cpu "${end_cpu:-0}" \
  --arg start_alloc "${start_alloc:-0}" \
  --arg end_alloc "${end_alloc:-0}" \
  --arg start_mallocs "${start_mallocs:-0}" \
  --arg end_mallocs "${end_mallocs:-0}" \
  --arg start_gc "${start_gc:-0}" \
  --arg end_gc "${end_gc:-0}" \
  --arg start_rss "${start_rss:-0}" \
  --arg end_rss "${end_rss:-0}" \
  --arg end_goroutines "${end_goroutines:-0}" \
  '{
    label: $label,
    load: $load,
    backend: $backend,
    process: {
      cpu_seconds: (($end_cpu | tonumber) - ($start_cpu | tonumber)),
      cpu_cores: ((($end_cpu | tonumber) - ($start_cpu | tonumber)) / $load.elapsed_seconds),
      allocated_bytes: (($end_alloc | tonumber) - ($start_alloc | tonumber)),
      mallocs: (($end_mallocs | tonumber) - ($start_mallocs | tonumber)),
      gc_cycles: (($end_gc | tonumber) - ($start_gc | tonumber)),
      starting_rss_bytes: ($start_rss | tonumber),
      ending_rss_bytes: ($end_rss | tonumber),
      ending_goroutines: ($end_goroutines | tonumber)
    }
  }'
