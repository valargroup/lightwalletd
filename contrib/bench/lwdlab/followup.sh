#!/usr/bin/env bash

set -euo pipefail

if [[ $# != 1 ]]; then
  echo "usage: followup.sh LAB_DIRECTORY" >&2
  exit 2
fi

lab_dir="$(cd "$1" && pwd)"
measure="$(cd "$(dirname "$0")" && pwd)/measure.sh"
duration="${LWD_LAB_DURATION:-5s}"
repeats="${LWD_LAB_REPEATS:-3}"
suite="${LWD_LAB_SUITE:-ranges}"
export LWD_LAB_SERVER_PROCS="${LWD_LAB_SERVER_PROCS:-2}"
result_dir="$lab_dir/followup-$suite-$(date +%Y%m%d-%H%M%S)"
mkdir -p "$result_dir"

run_one() {
  local label="$1" binary="$2" cache="$3"
  shift 3
  echo "Running $label" >&2
  "$measure" --label "$label" --server "$lab_dir/$binary" \
    --lab "$lab_dir/lwdlab" --data-dir "$lab_dir/$cache" \
    --output-dir "$result_dir" -- -concurrency 8 -duration "$duration" "$@" \
    >>"$result_dir/results.jsonl"
}

case "$suite" in
  ranges)
    for shape in mixed-sapling mixed-default segregated shielded; do
      args=()
      case "$shape" in
        mixed-sapling) cache=cache-dense; args=(-pools sapling);;
        mixed-default) cache=cache-dense;;
        segregated) cache=cache-segregated;;
        shielded) cache=cache-shielded;;
      esac
      for ((repeat=1; repeat<=repeats; repeat++)); do
        variants=(baseline filter selective)
        if ((repeat % 2 == 0)); then variants=(selective filter baseline); fi
        for variant in "${variants[@]}"; do
          case "$variant" in
            baseline) binary=lightwalletd-baseline;;
            filter) binary=lightwalletd-range-filter;;
            selective) binary=lightwalletd-selective;;
          esac
          run_one "$shape-$variant-$repeat" "$binary" "$cache" \
            -op range -start 100 -end 131 "${args[@]}"
        done
      done
    done
    ;;
  poll)
    for ((repeat=1; repeat<=repeats; repeat++)); do
      variants=(baseline keepalive poll combined)
      if ((repeat % 2 == 0)); then variants=(combined poll keepalive baseline); fi
      for variant in "${variants[@]}"; do
        case "$variant" in
          baseline) binary=lightwalletd-baseline;;
          keepalive) binary=lightwalletd-backend-keepalive;;
          poll) binary=lightwalletd-poll-cache;;
          combined) binary=lightwalletd-poll-keepalive;;
        esac
        run_one "wallet-poll-$variant-$repeat" "$binary" cache-dense \
          -op wallet-poll -height 2047
      done
    done
    ;;
  *) echo "unknown LWD_LAB_SUITE: $suite" >&2; exit 2;;
esac

echo "$result_dir/results.jsonl"
