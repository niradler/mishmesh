#!/usr/bin/env bash
set -u
source "$(dirname "${BASH_SOURCE[0]}")/env.sh"
svc="${1:-server-a}"
out="${2:-/dev/stdout}"
every="${3:-2}"
echo "ts_ms,rss_kb,threads,fds,cpu_ticks" > "$out"
while true; do
  line=$(dc exec -T "$svc" sh -c 'r=$(awk "/VmRSS/{print \$2}" /proc/1/status); t=$(awk "/Threads/{print \$2}" /proc/1/status); f=$(ls /proc/1/fd | wc -l); c=$(awk "{print \$14+\$15}" /proc/1/stat); echo "$r,$t,$f,$c"' 2>/dev/null) || line=",,,"
  echo "$(date +%s%3N),$line" >> "$out"
  sleep "$every"
done
