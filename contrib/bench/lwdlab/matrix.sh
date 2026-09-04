#!/usr/bin/env bash

set -euo pipefail

if [[ $# -ne 1 ]]; then
  echo "usage: matrix.sh LAB_DIRECTORY" >&2
  exit 2
fi

lab_dir="$1"
duration="${LWD_LAB_DURATION:-10s}"
repeats="${LWD_LAB_REPEATS:-3}"
workloads=",${LWD_LAB_WORKLOADS:-all},"
cooldown="${LWD_LAB_COOLDOWN:-0}"
lab="$lab_dir/lwdlab"
measure="$(cd "$(dirname "$0")" && pwd)/measure.sh"
result_dir="$lab_dir/results-$(date +%Y%m%d-%H%M%S)"
result_file="$result_dir/results.jsonl"
mkdir -p "$result_dir" "$lab_dir/nocache"

run_one() {
  local label="$1"
  local server="$2"
  local data_dir="$3"
  local warm_iterations="$4"
  local nocache="$5"
  shift 5

  echo "running $label" >&2
  local cache_args=()
  if [[ "$nocache" == true ]]; then
    cache_args+=(--nocache)
  fi
  "$measure" \
    --label "$label" \
    --server "$server" \
    --lab "$lab" \
    --data-dir "$data_dir" \
    --output-dir "$result_dir" \
    --warm-iterations "$warm_iterations" \
    "${cache_args[@]}" \
    -- "$@" | tee -a "$result_file"
  if [[ "$cooldown" != 0 ]]; then
    sleep "$cooldown"
  fi
}

run_pair() {
  local workload="$1"
  local candidate="$2"
  local data_dir="$3"
  local warm_iterations="$4"
  local nocache="$5"
  shift 5

  for ((repeat = 1; repeat <= repeats; repeat++)); do
    if ((repeat % 2 == 1)); then
      run_one "$workload-baseline-r$repeat" "$lab_dir/lightwalletd-baseline" "$data_dir" "$warm_iterations" "$nocache" "$@"
      run_one "$workload-candidate-r$repeat" "$candidate" "$data_dir" "$warm_iterations" "$nocache" "$@"
    else
      run_one "$workload-candidate-r$repeat" "$candidate" "$data_dir" "$warm_iterations" "$nocache" "$@"
      run_one "$workload-baseline-r$repeat" "$lab_dir/lightwalletd-baseline" "$data_dir" "$warm_iterations" "$nocache" "$@"
    fi
  done
}

run_selected() {
  local workload="$1"
  if [[ "$workloads" == ",all," || "$workloads" == *",$workload,"* ]]; then
    run_pair "$@"
  fi
}

run_selected backend-keepalive "$lab_dir/lightwalletd-backend-keepalive" "$lab_dir/cache-dense" 1 false \
  -op tree -concurrency 16 -duration "$duration" -height 500

run_selected direct-hex "$lab_dir/lightwalletd-direct-hex" "$lab_dir/nocache" 1 true \
  -op block -concurrency 4 -duration "$duration" -height 380640

run_selected range-filter "$lab_dir/lightwalletd-range-filter" "$lab_dir/cache-dense" 1 false \
  -op range -concurrency 16 -duration "$duration" -start 100 -end 131 -pools sapling

run_selected range-filter-default "$lab_dir/lightwalletd-range-filter" "$lab_dir/cache-dense" 1 false \
  -op range -concurrency 8 -duration "$duration" -start 100 -end 131

run_selected range-direct "$lab_dir/lightwalletd-range-direct" "$lab_dir/cache-sparse" 1 false \
  -op range -concurrency 32 -duration "$duration" -start 100 -end 355

run_selected range-direct-short "$lab_dir/lightwalletd-range-direct" "$lab_dir/cache-sparse" 1 false \
  -op range -concurrency 64 -duration "$duration" -start 100 -end 100

run_selected mempool-filter "$lab_dir/lightwalletd-mempool-filter" "$lab_dir/cache-dense" 1 false \
  -op mempool -concurrency 16 -duration "$duration" -mempool 4000 -exclude 3900

run_selected poll-cache "$lab_dir/lightwalletd-poll-cache" "$lab_dir/cache-dense" 3 false \
  -op poll -concurrency 16 -duration "$duration"

run_selected subtree-cache "$lab_dir/lightwalletd-subtree-cache" "$lab_dir/cache-dense" 1 false \
  -op subtree -concurrency 8 -duration "$duration" -subtrees 64

echo "$result_file"
