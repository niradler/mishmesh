#!/usr/bin/env bash
source "$(dirname "${BASH_SOURCE[0]}")/env.sh"
node="${1:-server-b}"
prefix="${2:-s}"
n="${3:-200}"
dc exec -T lt sh -c "for i in \$(seq 0 $((n-1))); do wget -q -S -O /dev/null --header \"Host: ${prefix}\$i.lt.test\" http://$node:8080/1k 2>&1 | awk '/HTTP\//{print \$2}'; done | sort | uniq -c | tr '\n' ' '; echo"
