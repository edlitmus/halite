#!/bin/sh
# sampler.sh NAME OUT [SECONDS]: every SECONDS (default 5), append one
# line about the newest process named NAME to OUT -- the hub on the hub
# host, simfleet on a driver. H6 of docs/simfleet-aws-plan.md.
#
# Why a script rather than the hub's own /v1/metrics: the hub exports no
# process metrics (no resident memory, threads or descriptors), so a hub
# on another host than the harness can only be measured where it runs
# (limit L7). This is what step 0 used on both hosts, with `ss -s` and
# the ENA bandwidth counters added.
#
# Columns: epoch, pid, rss_kib, fds, threads, cpu_s (utime+stime),
# established TCP sockets on the host, TIME_WAIT sockets, and the ENA
# allowance counters (conntrack available/exceeded, pps, bw in/out
# exceeded) where the driver exposes them; "-" where it does not.
# Linux only: it reads /proc. A FreeBSD hub (step 3F) needs its own.
set -u
name=$1; out=$2; every=${3:-5}
# pgrep -x matches the kernel's name for a process, which Linux cuts to
# 15 characters, so a longer NAME matches nothing and every row would be
# dashes. Found running this against amazon-ssm-agent (16) on a test host.
# halite-hub and simfleet are well inside it; refuse rather than sample
# nothing.
if [ ${#name} -gt 15 ]; then
  echo "sampler.sh: $name is longer than the 15 characters pgrep -x can match" >&2
  exit 2
fi
hz=$(getconf CLK_TCK)
iface=$(ip -o -4 route show to default | awk '{print $5; exit}')
printf 'epoch\tpid\trss_kib\tfds\tthreads\tcpu_s\testab\ttimewait\tconntrack_avail\tconntrack_exceeded\tpps_exceeded\tbw_in_exceeded\tbw_out_exceeded\n' > "$out"
ena() { echo "$stats" | awk -v k="$1" '$1 == k":" {print $2; f=1} END {if (!f) print "-"}'; }
while :; do
  pid=$(pgrep -nx "$name")
  if [ -n "$pid" ] && [ -r "/proc/$pid/stat" ]; then
    rss=$(awk '/^VmRSS/ {print $2}' "/proc/$pid/status")
    thr=$(awk '/^Threads/ {print $2}' "/proc/$pid/status")
    fds=$(ls "/proc/$pid/fd" 2>/dev/null | wc -l)
    cpu=$(awk -v hz="$hz" '{printf "%.2f", ($14+$15)/hz}' "/proc/$pid/stat")
  else
    pid=-; rss=-; thr=-; fds=-; cpu=-
  fi
  est=$(ss -Htn state established 2>/dev/null | wc -l)
  tw=$(ss -Htn state time-wait 2>/dev/null | wc -l)
  stats=$(ethtool -S "$iface" 2>/dev/null | sed 's/^ *//')
  printf '%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n' "$(date +%s)" "$pid" "$rss" "$fds" "$thr" "$cpu" "$est" "$tw" \
    "$(ena conntrack_allowance_available)" "$(ena conntrack_allowance_exceeded)" "$(ena pps_allowance_exceeded)" \
    "$(ena bw_in_allowance_exceeded)" "$(ena bw_out_allowance_exceeded)" >> "$out"
  sleep "$every"
done
