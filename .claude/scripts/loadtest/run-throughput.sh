#!/usr/bin/env bash
set -u
source "$(dirname "${BASH_SOURCE[0]}")/env.sh"
variant="${1:?variant label}"
agents="${2:-200}"
concs="${3:-50 200 1000}"
dur="${4:-10s}"
node="${5:-server-a}"
out="/out/t2-${variant}.jsonl"
export LT_TIMEOUT=120
dc exec -T lt sh -c "rm -f $out"
dc exec -T lt sh -c "pgrep -f 'lt backend' >/dev/null" || dc exec -T -d lt sh -c "/bin/w5/lt backend >/out/backend.log 2>&1"
dc exec -T -d lt sh -c "/bin/w5/lt agents -prefix q -steps $agents -rate 500 -final-hold 15m > /out/t2-agents-${variant}.log 2>&1"
sleep $((agents / 100 + 6))
for c in $concs; do
  [ "$variant" = direct ] || true
  ltx load -prefix q -agents "$agents" -addr "$node:8080" -c "$c" -d "$dur" -path /1k -size 1024 -label "${variant}-1k-c$c" | tee -a "$(printf %s "$W5_OUT")/t2-${variant}.jsonl"
  ltx load -prefix q -agents "$agents" -addr "$node:8080" -c "$c" -d "$dur" -path /1m -size 1048576 -label "${variant}-1m-c$c" | tee -a "$W5_OUT/t2-${variant}.jsonl"
done
dc exec -T lt sh -c "pkill -f 'lt agents' || true"
